package model

import (
	"encoding/json"
	"testing"
)

func TestWifiAPAliases(t *testing.T) {
	raw := []byte(`[
		{"macAddress":"AA-BB-CC-DD-EE-01","signalStrength":-60,"channel":6},
		{"bssid":"11:22:33:44:55:66","rssi":-72},
		"77:88:99:AA:BB:CC"
	]`)
	var scan WifiScanList
	if err := json.Unmarshal(raw, &scan); err != nil {
		t.Fatal(err)
	}
	if len(scan) != 3 {
		t.Fatalf("len=%d", len(scan))
	}
	if scan[0].MAC != "AA:BB:CC:DD:EE:01" || scan[0].RSSI != -60 || scan[0].Ch != 6 {
		t.Fatalf("ap0=%+v", scan[0])
	}
	if scan[1].MAC != "11:22:33:44:55:66" || scan[1].RSSI != -72 {
		t.Fatalf("ap1=%+v", scan[1])
	}
	if scan[2].MAC != "77:88:99:AA:BB:CC" {
		t.Fatalf("ap2=%+v", scan[2])
	}
}

func TestExtractWifiScanKeys(t *testing.T) {
	body := []byte(`{"sn":"S1","wifiAccessPoints":[{"macAddress":"aa:bb:cc:dd:ee:01","signalStrength":-50}]}`)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	scan, err := ExtractWifiScan(raw)
	if err != nil || len(scan) != 1 || scan[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("scan=%v err=%v", scan, err)
	}
}

func TestNormalizeGeolocate(t *testing.T) {
	u := DeviceUplink{
		Type: "geolocate",
		WifiScan: WifiScanList{
			{MAC: "aa:bb:cc:dd:ee:01", RSSI: -40},
		},
	}
	u.NormalizeLocation()
	if u.Type != "locate" || len(u.LocMacs) != 1 || u.LocType != LocTypeWiFi {
		t.Fatalf("%+v", u)
	}
}
