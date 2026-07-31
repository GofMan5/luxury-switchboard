package activity

import (
	tunnelapp "github.com/luxuryprivate/switchboard/backend/internal/slices/publictunnel/application"
	clientapp "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/application"
	clientdomain "github.com/luxuryprivate/switchboard/backend/internal/slices/tunnelclients/domain"
)

type Recorder struct{ service *clientapp.Service }

func NewRecorder(service *clientapp.Service) *Recorder { return &Recorder{service: service} }
func (recorder *Recorder) Queue(ip string, delta int)  { recorder.service.Queue(ip, delta) }
func (recorder *Recorder) Begin(value tunnelapp.ClientStart) string {
	return recorder.service.Begin(clientdomain.Start{IP: value.IP, Method: value.Method, Path: value.Path, Model: value.PublicModel, BytesIn: value.BytesIn})
}
func (recorder *Recorder) Finish(id string, value tunnelapp.ClientFinish) {
	recorder.service.Finish(id, clientdomain.Finish{Status: value.Status, BytesOut: value.BytesOut, ErrorCode: value.ErrorCode, Duration: value.Duration})
}
