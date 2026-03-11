package forgejowatch

import (
	"regexp"
	"strings"
)

var linkedReferencePattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+((?:[a-z0-9_.-]+/[a-z0-9_.-]+)?#\d+)`)
var genericReferencePattern = regexp.MustCompile(`(?i)(?:^|[\s(])((?:[a-z0-9_.-]+/[a-z0-9_.-]+)?#\d+)\b`)

func parseLinkedReferences(text, defaultOwner, defaultRepo string) []string {
	seen := make(map[string]struct{})
	var refs []string

	collect := func(matches [][]string) {
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			ref := normalizeReference(match[1], defaultOwner, defaultRepo)
			if ref == "" {
				continue
			}
			if _, ok := seen[ref]; ok {
				continue
			}
			seen[ref] = struct{}{}
			refs = append(refs, ref)
		}
	}

	collect(linkedReferencePattern.FindAllStringSubmatch(text, -1))
	if len(refs) == 0 {
		collect(genericReferencePattern.FindAllStringSubmatch(text, -1))
	}

	return refs
}

func normalizeReference(ref, defaultOwner, defaultRepo string) string {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "("))
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "#") {
		if defaultOwner == "" || defaultRepo == "" {
			return ""
		}
		return defaultOwner + "/" + defaultRepo + ref
	}
	parts := strings.SplitN(ref, "#", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "#" + parts[1]
}
