package config

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// unsetVar is a ${VAR} of the file without a value in the environment: unset,
// or set but empty.
type unsetVar struct {
	Name  string
	Unset bool // else set, but empty
}

func (v unsetVar) String() string {
	if v.Unset {
		return "${" + v.Name + "} isn't set"
	}
	return "${" + v.Name + "} is empty"
}

// unsetMarkRegexp matches the mark standing for an unsetVar, see unsetVars.
var unsetMarkRegexp = regexp.MustCompile(`__XTRADER_UNSET_(\d+)__`)

// unsetVars reads text, YAML (or JSON), as gobox/config does, but with each
// ${VAR} without a value expanded to a mark, and returns the settings that
// hold one, by their path, eg: database.dsn or services[0].env[1]. A ${VAR}
// out of the settings, eg: in a comment, is none of theirs.
func unsetVars(text string) (map[string][]unsetVar, error) {
	var vars []unsetVar
	marked := os.Expand(text, func(name string) string {
		value, set := os.LookupEnv(name)
		if value != "" {
			return value
		}
		vars = append(vars, unsetVar{Name: name, Unset: !set})
		return fmt.Sprintf("__XTRADER_UNSET_%d__", len(vars)-1)
	})
	if len(vars) == 0 {
		return nil, nil
	}
	var settings any
	if err := yaml.Unmarshal([]byte(marked), &settings); err != nil {
		return nil, err
	}
	found := make(map[string][]unsetVar)
	walkSettings("", settings, func(path, value string) {
		for _, match := range unsetMarkRegexp.FindAllStringSubmatch(value, -1) {
			if i, err := strconv.Atoi(match[1]); err == nil && i < len(vars) {
				found[path] = append(found[path], vars[i])
			}
		}
	})
	return found, nil
}

// walkSettings calls visit with every text of value, a setting of the YAML,
// and its path, the keys in lower case as viper reads them.
func walkSettings(path string, value any, visit func(path, value string)) {
	join := func(key any) string {
		if path == "" {
			return strings.ToLower(fmt.Sprint(key))
		}
		return path + "." + strings.ToLower(fmt.Sprint(key))
	}
	switch value := value.(type) {
	case map[string]any:
		for key, nested := range value {
			walkSettings(join(key), nested, visit)
		}
	case map[any]any:
		for key, nested := range value {
			walkSettings(join(key), nested, visit)
		}
	case []any:
		for i, nested := range value {
			walkSettings(fmt.Sprintf("%s[%d]", path, i), nested, visit)
		}
	case string:
		visit(path, value)
	}
}

// serviceEnvRegexp matches the path of an entry of the env of a service.
var serviceEnvRegexp = regexp.MustCompile(`^services\[(\d+)\]\.env\[(\d+)\]$`)

// checkUnset sorts the ${VAR} without a value of the settings of c: an error
// for the ones the controller can't do without, the URL of the database, the
// key of the secrets and an entry of the env of a service left empty, eg: the
// token of the bridge of MT5, which would start without one; a warning for any
// other unset, the ones of bootstrap excepted, removed once the first user is
// in.
func (c *Config) checkUnset(found map[string][]unsetVar) (errs []error, warnings []string) {
	paths := make([]string, 0, len(found))
	for path := range found {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		vars := found[path]
		required, owner := false, path
		switch {
		case path == "database.dsn" || path == "secrets_key":
			required = true
		case strings.HasPrefix(path, "bootstrap."):
			continue
		default:
			if m := serviceEnvRegexp.FindStringSubmatch(path); m != nil {
				i, _ := strconv.Atoi(m[1])
				j, _ := strconv.Atoi(m[2])
				if i < len(c.Services) && j < len(c.Services[i].Env) {
					name, value, _ := strings.Cut(c.Services[i].Env[j], "=")
					owner = fmt.Sprintf("service %s: env %s", c.Services[i].Name, name)
					required = value == ""
				}
			}
		}
		for _, v := range vars {
			switch {
			case required:
				errs = append(errs, fmt.Errorf("%s: %s", owner, v))
			case v.Unset:
				warnings = append(warnings, fmt.Sprintf("%s: %s, taken as empty", owner, v))
			}
		}
	}
	return errs, warnings
}
