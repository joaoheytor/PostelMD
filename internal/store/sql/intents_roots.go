//go:build linux

package sql

import "fmt"

// Roots returns the vault paths the intent names, each standing for itself and everything
// under it: a save's or create's file, a move's source and destination, a delete's item, a
// restore's destination. While the intent is open or flagged, the scan leaves those paths
// alone (ADR-0033, "The scan and intents"). A payload that doesn't decode is ErrBadIntent.
func (in Intent) Roots() ([]string, error) {
	var p interface {
		Payload
		roots() []BytePath
	}
	switch in.Kind {
	case IntentSave:
		p = &SaveIntent{}
	case IntentCreate:
		p = &CreateIntent{}
	case IntentMove:
		p = &MoveIntent{}
	case IntentDelete:
		p = &DeleteIntent{}
	case IntentRestore:
		p = &RestoreIntent{}
	default:
		return nil, fmt.Errorf("%w: kind %q", ErrBadIntent, in.Kind)
	}
	if err := in.Decode(p); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range p.roots() {
		out = append(out, string(r))
	}
	return out, nil
}

func (p *SaveIntent) roots() []BytePath    { return []BytePath{p.Path} }
func (p *CreateIntent) roots() []BytePath  { return []BytePath{p.Path} }
func (p *MoveIntent) roots() []BytePath    { return []BytePath{p.From, p.To} }
func (p *DeleteIntent) roots() []BytePath  { return []BytePath{p.Path} }
func (p *RestoreIntent) roots() []BytePath { return []BytePath{p.To} }

// Under reports whether path is root or under it: what Roots' "everything under it" means.
func Under(path, root string) bool { return path == root || under(path, root) }
