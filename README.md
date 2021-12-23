## Vehicle Information Service
[![CI](https://github.com/aoscloud/aos_vis/workflows/CI/badge.svg)](https://github.com/aoscloud/aos_vis/actions?query=workflow%3ACI)


aos_vis is web socket server which provides vehicle information data using W3C protocol

https://www.w3.org/TR/vehicle-information-service/

### Build

Build plugins:

```
go build -buildmode=plugin plugins/storageadapter/storageadapter.go
go build -buildmode=plugin plugins/telemetryemulatoradapter/telemetryemulatoradapter.go
go build -buildmode=plugin plugins/renesassimulatoradapter/renesassimulatoradapter.go
```

Build main program:

```
go build
```

### Configure

Specify parameters in visconfig.json file:

```json
{
	"ServerURL": ":443",
	"VISCert": "data/wwwivi.crt.pem",
	"VISKey": "data/wwwivi.key.pem",
	"Adapters":[
		{
			"Plugin":"telemetryemulatoradapter.so",
			"Params": {
				"SensorURL":"http://sensors:8800"
			}
		},{
			"Plugin":"storageadapter.so",
			"Params": {
				"Data" : {
					"Attribute.Vehicle.VehicleIdentification.VIN": {"Value": "TestVIN", "Public": true, "ReadOnly": true},
					"Attribute.Vehicle.UserIdentification.Users":  {"Value": ["User1", "Provider1"], "Public": true},
					"Attribute.Car.Message":  {"Public": true}
				}
			}
		}
	]
}
```

Put root certificate rootCA.crt.pem to /etc/ssl/certs in your system.
