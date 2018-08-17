package dataprovider

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"gitpct.epam.com/epmd-aepr/aos_vis/config"
	"gitpct.epam.com/epmd-aepr/aos_vis/dataadapter"
)

/*******************************************************************************
 * Types
 ******************************************************************************/

// DataProvider interface for geeting vehicle data
type DataProvider struct {
	sensors          map[string]*sensorDescription
	currentSubsID    uint64
	subscribeInfoMap map[uint64]*subscribeInfo
	mutex            sync.Mutex
}

// AuthInfo authorization info
type AuthInfo struct {
	IsAuthorized bool
	Permissions  map[string]string
}

type sensorDescription struct {
	adapter      dataadapter.DataAdapter
	subscribeIds *list.List
}

type subscribeInfo struct {
	channel chan<- interface{}
	path    string
}

/*******************************************************************************
 * Public
 ******************************************************************************/

// New returns pointer to DataProvider
func New(config *config.Config) (provider *DataProvider, err error) {
	log.Debug("Create data provider")

	provider = &DataProvider{}

	provider.sensors = make(map[string]*sensorDescription)
	provider.subscribeInfoMap = make(map[uint64]*subscribeInfo)

	adapterCount := 0

	for _, adapterCfg := range config.Adapters {
		if err = provider.createAdapter(adapterCfg.Name, adapterCfg.Params); err != nil {
			return nil, err
		}

		adapterCount++
	}

	if adapterCount == 0 {
		return nil, errors.New("No valid adapter info provided")
	}

	return provider, nil
}

// GetData returns VIS data
func (provider *DataProvider) GetData(path string, authInfo *AuthInfo) (data interface{}, err error) {
	log.WithField("path", path).Debug("Get data")

	filter, err := createRegexpFromPath(path)
	if err != nil {
		return data, err
	}

	// Create map of pathes grouped by adapter
	adapterDataMap := make(map[dataadapter.DataAdapter][]string)

	for path, sensor := range provider.sensors {
		if filter.MatchString(path) {
			if err = checkPermissions(sensor.adapter, path, authInfo, "r"); err != nil {
				return data, err
			}

			if adapterDataMap[sensor.adapter] == nil {
				adapterDataMap[sensor.adapter] = make([]string, 0, 10)
			}
			adapterDataMap[sensor.adapter] = append(adapterDataMap[sensor.adapter], path)
		}
	}

	// Create common data array
	commonData := make(map[string]interface{})

	for adapter, pathList := range adapterDataMap {
		result, err := adapter.GetData(pathList)
		if err != nil {
			return data, err
		}
		for path, value := range result {
			log.WithFields(log.Fields{"adapter": adapter.GetName(), "value": value, "path": path}).Debug("Data from adapter")

			commonData[path] = value
		}
	}

	if len(commonData) == 0 {
		return data, errors.New("The specified data path does not exist")
	}

	return convertData(path, commonData), nil
}

// SetData sets VIS data
func (provider *DataProvider) SetData(path string, data interface{}, authInfo *AuthInfo) (err error) {
	log.WithFields(log.Fields{"path": path, "data": data}).Debug("Set data")

	filter, err := createRegexpFromPath(path)
	if err != nil {
		return err
	}

	// Create map from data. According to VIS spec data could be array of map,
	// map or simple value. Convert array of map to map and keep map as is.
	suffixMap := make(map[string]interface{})

	switch data.(type) {
	// convert array of map to map
	case []interface{}:
		for _, arrayItem := range data.([]interface{}) {
			arrayMap, ok := arrayItem.(map[string]interface{})
			if ok {
				for path, value := range arrayMap {
					suffixMap[path] = value
				}
			}
		}

	// keep map as is
	case map[string]interface{}:
		suffixMap = data.(map[string]interface{})
	}

	// adapterDataMap contains VIS data grouped by adapters
	adapterDataMap := make(map[dataadapter.DataAdapter]map[string]interface{})

	for path, sensor := range provider.sensors {
		if filter.MatchString(path) {
			var value interface{}
			if len(suffixMap) != 0 {
				// if there is suffix map, try to find proper path by suffix
				for suffix, v := range suffixMap {
					if strings.HasSuffix(path, suffix) {
						value = v
						break
					}
				}
			} else {
				// For simple value set data
				value = data
			}

			if value != nil {
				// Set data to adapterDataMap
				log.WithFields(log.Fields{"adapter": sensor.adapter.GetName(), "value": value, "path": path}).Debug("Set data to adapter")

				if err = checkPermissions(sensor.adapter, path, authInfo, "w"); err != nil {
					return err
				}

				if adapterDataMap[sensor.adapter] == nil {
					adapterDataMap[sensor.adapter] = make(map[string]interface{})
				}
				adapterDataMap[sensor.adapter][path] = value
			}
		}
	}

	// If adapterMap is empty: no path found
	if len(adapterDataMap) == 0 {
		return errors.New("The server is unable to fulfil the client request because the request is malformed")
	}

	// Everything ok: try to set to adapter
	for adapter, visData := range adapterDataMap {
		if err = adapter.SetData(visData); err != nil {
			return err
		}
	}

	return nil
}

// Subscribe subscribes for data change
func (provider *DataProvider) Subscribe(path string, authInfo *AuthInfo) (id uint64, channel <-chan interface{}, err error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	log.WithField("path", path).Debug("Subscribe")

	filter, err := createRegexpFromPath(path)
	if err != nil {
		return id, channel, err
	}

	// Create map of pathes grouped by adapter
	subscribeMap := make(map[dataadapter.DataAdapter][]string)

	// Get data from adapter and group it by parent
	for path, sensor := range provider.sensors {
		if filter.MatchString(path) {
			if err = checkPermissions(sensor.adapter, path, authInfo, "r"); err != nil {
				return id, channel, err
			}

			// Add subscribe id to subscribe list
			sensor.subscribeIds.PushBack(provider.currentSubsID)

			// Add path to subscribeMap
			if subscribeMap[sensor.adapter] == nil {
				subscribeMap[sensor.adapter] = make([]string, 0, 10)
			}
			subscribeMap[sensor.adapter] = append(subscribeMap[sensor.adapter], path)
		}
	}

	// Subscribe for adapter data changes
	for adapter, pathList := range subscribeMap {
		for _, path := range pathList {
			log.WithFields(log.Fields{"adapter": adapter.GetName(), "path": path}).Debug("Subscribe for adapter data")
		}

		if err = adapter.Subscribe(pathList); err != nil {
			return id, channel, err
		}
	}

	id = provider.currentSubsID

	dataChannel := make(chan interface{}, 100)
	provider.subscribeInfoMap[id] = &subscribeInfo{channel: dataChannel, path: path}

	provider.currentSubsID++

	return id, dataChannel, nil
}

// Unsubscribe unsubscribes from data change
func (provider *DataProvider) Unsubscribe(id uint64, authInfo *AuthInfo) (err error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	log.WithField("subscribeID", id).Debug("Unsubscribe")

	subscribeInfo, ok := provider.subscribeInfoMap[id]
	if !ok {
		return fmt.Errorf("Subscribe id %v not found", id)
	}
	close(subscribeInfo.channel)
	delete(provider.subscribeInfoMap, id)

	// Create map of pathes grouped by adapter
	unsubscribeMap := make(map[dataadapter.DataAdapter][]string)

	// Go through all sensors and remove id
	for path, sensor := range provider.sensors {
		if sensor.subscribeIds.Len() == 0 {
			continue
		}

		var nextElement *list.Element

		for idElement := sensor.subscribeIds.Front(); idElement != nil; idElement = nextElement {
			nextElement = idElement.Next()
			if idElement.Value.(uint64) == id {
				sensor.subscribeIds.Remove(idElement)
			}
		}

		if sensor.subscribeIds.Len() == 0 {
			// Add path to unsubscribeMap
			if unsubscribeMap[sensor.adapter] == nil {
				unsubscribeMap[sensor.adapter] = make([]string, 0, 10)
			}
			unsubscribeMap[sensor.adapter] = append(unsubscribeMap[sensor.adapter], path)
		}
	}

	// Unsubscribe from adapter data changes
	for adapter, pathList := range unsubscribeMap {
		for _, path := range pathList {
			log.WithFields(log.Fields{"adapter": adapter.GetName(), "path": path}).Debug("Unsubscribe from adapter data")
		}

		if err = adapter.Unsubscribe(pathList); err != nil {
			return err
		}
	}

	return nil
}

// GetSubscribeIDs returns list of active subscribe ID
func (provider *DataProvider) GetSubscribeIDs() (result []uint64) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()

	result = make([]uint64, 0, len(provider.subscribeInfoMap))

	for id := range provider.subscribeInfoMap {
		result = append(result, id)
	}

	return result
}

/*******************************************************************************
 * Private
 ******************************************************************************/

func (provider *DataProvider) createAdapter(name string, params interface{}) (err error) {
	var adapter dataadapter.DataAdapter

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}

	switch name {
	case "TestAdapter":
		if adapter, err = dataadapter.NewTestAdapter(); err != nil {
			return err
		}

	case "SensorEmulatorAdapter":
		if adapter, err = dataadapter.NewSensorEmulatorAdapter(paramsJSON); err != nil {
			return err
		}

	case "MessageAdapter":
		if adapter, err = dataadapter.NewMessageAdapter(); err != nil {
			return err
		}

	default:
		return errors.New("Unknown adapter name: " + name)
	}

	pathList, err := adapter.GetPathList()
	if err != nil {
		return err
	}

	for _, path := range pathList {
		if _, ok := provider.sensors[path]; ok {
			log.WithField("path", path).Warningf("Path already in adapter map")
		} else {
			log.WithFields(log.Fields{"path": path, "adaptor": adapter.GetName()}).Debug("Add path")

			provider.sensors[path] = &sensorDescription{adapter: adapter, subscribeIds: list.New()}
		}
	}

	go provider.handleSubscribeChannel(adapter.GetSubscribeChannel())

	return nil
}

func (provider *DataProvider) handleSubscribeChannel(channel <-chan map[string]interface{}) {
	for {
		changes, more := <-channel
		if !more {
			return
		}

		// Group data by subscribe ids
		subscribeDataMap := make(map[uint64]map[string]interface{})

		for path, value := range changes {
			for idElement := provider.sensors[path].subscribeIds.Front(); idElement != nil; idElement = idElement.Next() {
				id := idElement.Value.(uint64)

				if subscribeDataMap[id] == nil {
					subscribeDataMap[id] = make(map[string]interface{})
				}

				subscribeDataMap[idElement.Value.(uint64)][path] = value
			}
		}

		// Notify subscribers by id
		for id, data := range subscribeDataMap {
			for path, value := range data {
				log.WithFields(log.Fields{
					"subscribeID": id,
					"path":        path,
					"value":       value}).Debug("Notify subscribers")
			}
			provider.subscribeInfoMap[id].channel <- convertData(provider.subscribeInfoMap[id].path, data)
		}
	}
}

func getParentPath(path string) (parent string) {
	return path[:strings.LastIndex(path, ".")]
}

func createRegexpFromPath(path string) (exp *regexp.Regexp, err error) {
	regexpStr := strings.Replace(path, ".", "[.]", -1)
	regexpStr = strings.Replace(regexpStr, "*", ".*?", -1)
	regexpStr = "^" + regexpStr
	exp, err = regexp.Compile(regexpStr)

	return exp, err
}

func checkPermissions(adapter dataadapter.DataAdapter, path string, authInfo *AuthInfo, permissions string) (err error) {
	if authInfo == nil {
		return nil
	}

	isPublic, err := adapter.IsPathPublic(path)
	if err != nil {
		return err
	}
	if !authInfo.IsAuthorized && !isPublic {
		return errors.New("Client is not authorized")
	}
	if isPublic {
		return nil
	}

	// Check permission
	for mask, value := range authInfo.Permissions {
		filter, err := createRegexpFromPath(mask)
		if err != nil {
			return err
		}

		if filter.MatchString(path) && strings.Contains(strings.ToLower(value), strings.ToLower(permissions)) {
			log.WithFields(log.Fields{
				"path":        path,
				"permissions": value}).Debug("Data permissions")

			return nil
		}
	}

	return errors.New("Client does not have permissions")
}

func convertData(requestedPath string, data map[string]interface{}) (result interface{}) {
	// Group by parent map[parent] -> (map[path] -> value)
	parentDataMap := make(map[string]map[string]interface{})

	for path, value := range data {
		parent := getParentPath(path)
		if parentDataMap[parent] == nil {
			parentDataMap[parent] = make(map[string]interface{})
		}
		parentDataMap[parent][path] = value
	}

	// make array from map
	dataArray := make([]map[string]interface{}, 0, len(parentDataMap))
	for _, value := range parentDataMap {
		dataArray = append(dataArray, value)
	}

	// VIS defines 3 forms of returning result:
	// * simple value if it is one signal
	// * map[path]value if result belongs to same parent
	// * []map[path]value if result belongs to different parents
	//
	// TODO: It is unclear from spec how to combine results in one map.
	// By which criteria we should put data to one map or to array element.
	// For now it is combined by parent node.

	if len(dataArray) == 1 {
		if len(dataArray[0]) == 1 {
			for path, value := range dataArray[0] {
				if path == requestedPath {
					// return simple value
					return value
				}
			}
		}
		// return map of same parent
		return dataArray[0]
	}
	// return array of different parents
	return dataArray
}
