package plugins

import (
	"encoding/json"
	"fmt"

	"glitched/internal/api"
)

// AccessLevels are the access picker's levels by name, as
// threads.setAccess and threads.create take them.
var AccessLevels = map[string]api.Access{
	"Ask for approval": api.AccessAsk,
	"Approve for me":   api.AccessAutoEdits,
	"Full access":      api.AccessFull,
}

// CreateAccess is the access a threads.create starts its thread at: the
// level it names, which the host gated on threads:access, else Ask for
// approval, never what the user picked last.
func CreateAccess(name *string) (api.Access, error) {
	if name == nil {
		return api.AccessAsk, nil
	}
	level, ok := AccessLevels[*name]
	if !ok {
		return 0, fmt.Errorf("%q is no access; threads.create takes \"Ask for approval\", \"Approve for me\" or \"Full access\"", *name)
	}
	return level, nil
}

// needs are the capabilities effect e needs: its type's, and
// threads:access for a threads.create that names an access.
func needs(e Effect) []string {
	need := []string{Effects[e.Type]}
	if e.Type == "threads.create" {
		var a struct{ Access *string }
		if json.Unmarshal(e.Args, &a) != nil || a.Access != nil {
			need = append(need, "threads:access")
		}
	}
	return need
}
