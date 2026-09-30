package acpruntime

import "sort"

// ConfigApplicationReport records the selected session configuration separately
// from credential-bearing provider launch environment. It is an in-memory
// report, not an assertion that a provider enforces OS sandbox permissions.
type ConfigApplicationReport struct {
	Items []ConfigApplicationItem
	Error string
}
type ConfigApplicationItem struct {
	Key              string
	Source           string
	Requested        any
	Effective        any
	Applied          bool
	Unsupported      bool
	Rejected         bool
	RequiresRestart  bool
	ProviderReadback any
}

func configurationReport(request InitialConfig, applied InitialConfigReport, err error, metadata RuntimeSessionMetadata, authoritative bool) ConfigApplicationReport {
	report := ConfigApplicationReport{}
	values := map[string]any{"mode": request.Mode, "model": request.Model, "effort": request.Effort}
	for key, value := range request.Raw {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := values[key]
		if value == nil {
			continue
		}
		item := ConfigApplicationItem{Key: key, Source: "InitialConfig", Requested: cloneOwned(value)}
		effectiveID := key
		for _, done := range applied.Applied {
			if done.Key == key {
				effectiveID = done.ID
				item.Applied = done.Reason == ""
				item.Unsupported = done.Reason != ""
				item.Effective = cloneOwned(done.Value)
			}
		}
		if !authoritative {
			item.ProviderReadback = nil
		} else if key == "mode" {
			item.ProviderReadback = metadata.CurrentModeID
		} else {
			for _, option := range metadata.AgentConfigOptions {
				if option.ID == effectiveID || option.Category == key {
					item.ProviderReadback = cloneOwned(option.Value)
				}
			}
		}
		item.Rejected = err != nil && !item.Applied
		report.Items = append(report.Items, item)
	}
	if err != nil {
		report.Error = err.Error()
	}
	return report
}
