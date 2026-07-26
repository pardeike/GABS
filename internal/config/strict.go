package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// scanDuplicateMembers token-scans raw JSON for duplicate object members.
// Both struct and map decoding silently keep the last member, so a
// duplicated profile name, hook field, or env key would validate while
// launching a different context than the one a reviewer read — unacceptable
// with automatic reload. Detection must happen before any decoded form is
// accepted.
func scanDuplicateMembers(data []byte) ([]ConfigIssue, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var issues []ConfigIssue

	var walkValue func(path string) error
	walkValue = func(path string) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil // scalar
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := keyTok.(string)
				kp := path + "/" + escapePointerToken(key)
				if seen[key] {
					issues = append(issues, ConfigIssue{Path: kp, Message: "duplicate JSON member"})
				}
				seen[key] = true
				if err := walkValue(kp); err != nil {
					return err
				}
			}
			_, err := dec.Token() // consume '}'
			return err
		case '[':
			i := 0
			for dec.More() {
				if err := walkValue(fmt.Sprintf("%s/%d", path, i)); err != nil {
					return err
				}
				i++
			}
			_, err := dec.Token() // consume ']'
			return err
		}
		return nil
	}

	if err := walkValue(""); err != nil {
		return nil, err
	}
	return issues, nil
}

// Known-key tables. Unknown keys inside the new subtrees (profiles,
// launchInputs, lifecycle) are errors; unknown keys elsewhere are warnings
// naming the path — protection against silent typos without breaking old
// files.
var (
	knownTopKeys = keySet("version", "games", "toolNormalization", "apiKey", "portRanges", "timeouts", "stripOutputSchema")
	knownToolN   = keySet("enableOpenAINormalization", "maxToolNameLength", "preserveOriginalName")
	knownPorts   = keySet("customRanges")
	knownRange   = keySet("min", "max")
	knownTOs     = keySet("startup", "session")
	knownStartup = keySet("processStartSeconds", "gabpConnectSeconds")
	knownSession = keySet("ownerLeaseSeconds")
	knownGame    = keySet("id", "name", "launchMode", "target", "args", "workingDir",
		"stopProcessName", "gabpMode", "description",
		"env", "unsetEnv", "defaultProfile", "profiles", "launchInputs", "lifecycle")
	knownProfile   = keySet("description", "args", "env", "unsetEnv", "workingDir", "lifecycle")
	knownInput     = keySet("description", "type", "enum", "minimum", "maximum", "maxLength", "pattern", "profiles", "args", "env")
	knownLifecycle = keySet("status", "stop", "kill")
	knownHook      = keySet("command", "args", "workingDir", "env", "unsetEnv",
		"timeoutSeconds", "verifyTimeoutSeconds", "runningExitCodes", "stoppedExitCodes")
)

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// checkUnknownKeys walks the decoded document against the known-key tables.
func checkUnknownKeys(data []byte) (errs, warns []ConfigIssue, fatal error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, err
	}

	check := func(path string, m map[string]any, known map[string]bool, strict bool) {
		for _, k := range sortedKeys(m) {
			if known[k] {
				continue
			}
			issue := ConfigIssue{Path: path + "/" + escapePointerToken(k), Message: "unknown key"}
			if strict {
				errs = append(errs, issue)
			} else {
				warns = append(warns, issue)
			}
		}
	}
	obj := func(v any) (map[string]any, bool) {
		m, ok := v.(map[string]any)
		return m, ok
	}

	var walkLifecycle func(path string, v any)
	walkLifecycle = func(path string, v any) {
		lc, ok := obj(v)
		if !ok {
			return
		}
		check(path, lc, knownLifecycle, true)
		for _, slot := range []string{"status", "stop", "kill"} {
			if hv, ok := obj(lc[slot]); ok {
				check(path+"/"+slot, hv, knownHook, true)
			}
		}
	}

	check("", root, knownTopKeys, false)
	if tn, ok := obj(root["toolNormalization"]); ok {
		check("/toolNormalization", tn, knownToolN, false)
	}
	if pr, ok := obj(root["portRanges"]); ok {
		check("/portRanges", pr, knownPorts, false)
		if ranges, ok := pr["customRanges"].([]any); ok {
			for i, r := range ranges {
				if rm, ok := obj(r); ok {
					check(fmt.Sprintf("/portRanges/customRanges/%d", i), rm, knownRange, false)
				}
			}
		}
	}
	if tos, ok := obj(root["timeouts"]); ok {
		check("/timeouts", tos, knownTOs, false)
		if st, ok := obj(tos["startup"]); ok {
			check("/timeouts/startup", st, knownStartup, false)
		}
		if se, ok := obj(tos["session"]); ok {
			check("/timeouts/session", se, knownSession, false)
		}
	}
	if games, ok := obj(root["games"]); ok {
		for _, id := range sortedKeys(games) {
			gm, ok := obj(games[id])
			if !ok {
				continue
			}
			gpath := "/games/" + escapePointerToken(id)
			check(gpath, gm, knownGame, false)
			if profiles, ok := obj(gm["profiles"]); ok {
				for _, pname := range sortedKeys(profiles) {
					if pm, ok := obj(profiles[pname]); ok {
						ppath := gpath + "/profiles/" + escapePointerToken(pname)
						check(ppath, pm, knownProfile, true)
						if lv, ok := pm["lifecycle"]; ok {
							walkLifecycle(ppath+"/lifecycle", lv)
						}
					}
				}
			}
			if inputs, ok := obj(gm["launchInputs"]); ok {
				for _, iname := range sortedKeys(inputs) {
					if im, ok := obj(inputs[iname]); ok {
						check(gpath+"/launchInputs/"+escapePointerToken(iname), im, knownInput, true)
					}
				}
			}
			if lv, ok := gm["lifecycle"]; ok {
				walkLifecycle(gpath+"/lifecycle", lv)
			}
		}
	}
	return errs, warns, nil
}
