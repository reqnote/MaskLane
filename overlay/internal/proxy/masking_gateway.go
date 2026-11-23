package proxy

import (
	"net/http"
	"time"

	"github.com/luckyPipewrench/pipelock/internal/redact"
)

// MaskingDecision carries only metadata from one inspected request.
type MaskingDecision struct {
	At      time.Time
	State   string
	Reason  string
	Company int
	Project int
}

// MaskingGateway gets an immutable dictionary snapshot for each request.
// A nil matcher means a dictionary has not yet been configured.
type MaskingGateway struct {
	Matcher func() *redact.Matcher
	Record  func(MaskingDecision)
}

func WithMaskingGateway(gateway MaskingGateway) Option {
	return func(p *Proxy) { p.maskingGateway = &gateway }
}

type maskingRequest struct {
	gateway   *MaskingGateway
	matcher   *redact.Matcher
	attempted bool
	sent      bool
	recorded  bool
}

func (gateway *MaskingGateway) begin(r *http.Request) *maskingRequest {
	carriesBody := r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
	if !carriesBody && r.Header.Get("Upgrade") == "" && r.Method != http.MethodConnect {
		return nil
	}
	return &maskingRequest{gateway: gateway, matcher: gateway.Matcher()}
}

func (r *maskingRequest) finish(report *redact.Report) {
	if r.recorded {
		return
	}
	r.recorded = true
	record := MaskingDecision{At: time.Now(), State: "blocked", Reason: "inspection"}
	switch {
	case r.sent:
		record.State, record.Reason = "sent", ""
		record.Company = report.ByClass["company"]
		record.Project = report.ByClass["project"]
	case r.attempted:
		record.State, record.Reason = "unconfirmed", "upstream"
	case r.matcher == nil:
		record.Reason = "no_mapping"
	case report != nil && report.Applied:
		record.Reason = "policy"
	}
	r.gateway.Record(record)
}
