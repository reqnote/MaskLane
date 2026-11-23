package proxy

import (
	"net/http"
	"time"

	"github.com/luckyPipewrench/pipelock/internal/redact"
)

// MaskingDecision carries counts and the generated tokens of one inspected
// request. Tokens are keyed by rule index at MappingRevision.
type MaskingDecision struct {
	At              time.Time
	State           string
	Reason          string
	Company         int
	Project         int
	Other           int
	Person          int
	Confidential    int
	MappingRevision uint64
	Automatic       map[int]string
}

type MaskingRules struct {
	Matcher         *redact.Matcher
	Revision        uint64
	AutomaticTokens func(*redact.Redactor) map[int]string
}

// MaskingGateway gets an immutable dictionary snapshot for each request.
// A nil matcher means a dictionary has not yet been configured.
type MaskingGateway struct {
	Rules  func() MaskingRules
	Record func(MaskingDecision)
}

func WithMaskingGateway(gateway MaskingGateway) Option {
	return func(p *Proxy) { p.maskingGateway = &gateway }
}

type maskingRequest struct {
	gateway   *MaskingGateway
	matcher   *redact.Matcher
	revision  uint64
	tokens    func(*redact.Redactor) map[int]string
	automatic map[int]string
	attempted bool
	sent      bool
	recorded  bool
}

func (gateway *MaskingGateway) begin(r *http.Request) *maskingRequest {
	carriesBody := r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
	if !carriesBody && r.Header.Get("Upgrade") == "" && r.Method != http.MethodConnect {
		return nil
	}
	rules := gateway.Rules()
	return &maskingRequest{gateway: gateway, matcher: rules.Matcher, revision: rules.Revision, tokens: rules.AutomaticTokens}
}

func (r *maskingRequest) captureTokens(redactor *redact.Redactor) {
	if r != nil && r.tokens != nil {
		r.automatic = r.tokens(redactor)
		r.tokens = nil
	}
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
		record.Other = report.ByClass["other"]
		record.Person = report.ByClass["person"]
		record.Confidential = report.ByClass["confidential"]
		record.MappingRevision = r.revision
		record.Automatic = r.automatic
	case r.attempted:
		record.State, record.Reason = "unconfirmed", "upstream"
	case r.matcher == nil:
		record.Reason = "no_mapping"
	case report != nil && report.Applied:
		record.Reason = "policy"
	}
	r.gateway.Record(record)
}
