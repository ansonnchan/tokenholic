package normalize

// CapabilitySupport describes how reliably a source can provide one field.
type CapabilitySupport string

const (
	// CapabilityReported means the source records the field directly.
	CapabilityReported CapabilitySupport = "reported"
	// CapabilityDerived means tokenholic computes the field without estimation.
	CapabilityDerived CapabilitySupport = "derived"
	// CapabilityInferred means surrounding source context supplies the field.
	CapabilityInferred CapabilitySupport = "inferred"
	// CapabilityUnsupported means compatible records do not expose the field.
	CapabilityUnsupported CapabilitySupport = "unsupported"
)

// Capabilities reports field-level coverage independently from usage values.
type Capabilities struct {
	Model           CapabilitySupport `json:"model"`
	InputUncached   CapabilitySupport `json:"input_uncached_tokens"`
	InputCacheRead  CapabilitySupport `json:"input_cache_read_tokens"`
	InputCacheWrite CapabilitySupport `json:"input_cache_write_tokens"`
	Output          CapabilitySupport `json:"output_tokens"`
	ReasoningOutput CapabilitySupport `json:"reasoning_output_tokens"`
}

// HealthStatus classifies discovery and accessibility of a local evidence root.
type HealthStatus string

const (
	// HealthDetected means compatible candidate records are available.
	HealthDetected HealthStatus = "detected"
	// HealthNotFound means the expected evidence root does not exist.
	HealthNotFound HealthStatus = "not_found"
	// HealthPermissionDenied means discovery cannot read the evidence root.
	HealthPermissionDenied HealthStatus = "permission_denied"
	// HealthUnsupported means the root exists in an incompatible form.
	HealthUnsupported HealthStatus = "unsupported"
	// HealthDegraded means the root is readable but has no candidate records.
	HealthDegraded HealthStatus = "degraded"
	// HealthError means discovery failed for another reason.
	HealthError HealthStatus = "error"
	// HealthUnavailable means the source's records exist but this install
	// cannot read them, for a reason given as a warning code. It is how a
	// day-level source is reported when the program that reads it is
	// absent or turned off. Its usage is unknown, never zero.
	HealthUnavailable HealthStatus = "unavailable"
)

// SourceHealth is the redaction-safe result of discovering one source root.
type SourceHealth struct {
	Source       Source       `json:"source"`
	Surface      Surface      `json:"surface"`
	Status       HealthStatus `json:"status"`
	RecordCount  *uint64      `json:"record_count"`
	Format       string       `json:"format,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
	Warnings     []Warning    `json:"warnings,omitempty"`
}
