package main

import (
	"encoding/json"

	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

func viaOutbound(link string) (json.RawMessage, error) {
	l, err := xray.ParseLink(link)
	if err != nil {
		return nil, err
	}
	return json.Marshal(l.Outbound)
}

func startVia(raw json.RawMessage) (*xray.Instance, error) {
	var ob xray.Outbound
	if err := json.Unmarshal(raw, &ob); err != nil {
		return nil, err
	}
	return xray.Start(nil, ob, "")
}
