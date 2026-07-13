package cek

import (
	"context"
	"errors"
	"math/big"
	"time"
)

// RestartChoice is the operator/agent choice delivered to a parked machine
// (ADR-05 §6): the restart name plus its arguments.
type RestartChoice struct {
	Name string
	Args map[string]any
}

// Resume re-enters a parked machine from a decoded State, delivering the chosen
// restart (ADR-05 §6). A fuel park resumes at the exact suspended transition
// under a fresh budget (grant-fuel supplies it); a signal park delivers the
// restart value as the awaited value of the parked call.
func (in *Interp) Resume(ctx context.Context, st *State, choice RestartChoice) Outcome {
	if choice.Name == "abort" {
		return Outcome{Kind: OutFaulted, Fault: strVal("aborted")}
	}
	root, err := in.loadAST(st.DefHash)
	if err != nil {
		return Outcome{Kind: OutError, Err: err}
	}
	if err := in.rebindNodes(st); err != nil {
		return Outcome{Kind: OutError, Err: err}
	}
	node, _ := navigate(root, st.Path)
	m := &machine{
		in:        in,
		defHash:   st.DefHash,
		root:      root,
		node:      node,
		path:      st.Path,
		env:       st.Env,
		kont:      st.Kont,
		mode:      st.Mode,
		val:       st.Val,
		sig:       st.Sig,
		host:      &Host{ctx: ctx, reg: in.reg},
		tier:      st.Tier,
		fuelSteps: st.FuelSteps,
		fuelAlloc: st.FuelAlloc,
	}

	switch st.ParkKind {
	case ParkSignal:
		// Deliver the restart value at the parked call point.
		m.val = restartValue(choice)
		m.mode = ModeApply
	}

	switch st.Tier {
	case TierTrusted:
		gm := &governorMeter{ceiling: DefaultGovernorCeiling, deadline: time.Now().Add(DefaultGovernorWall)}
		return run[*governorMeter](m, gm)
	default:
		steps := grantedFuel(choice, st.FuelSteps)
		alloc := st.FuelAlloc
		if alloc == 0 {
			alloc = DefaultFuelAllocBytes
		}
		m.fuelSteps = steps
		m.fuelAlloc = alloc
		fm := &fuelMeter{steps: steps, alloc: alloc}
		return run[*fuelMeter](m, fm)
	}
}

// rebindNodes re-derives every K frame's live Node pointer from its immortal
// Path (frames store only Paths on the wire). Each K segment executes in the
// definition entered at its call: the top segment is st.DefHash; a FrRet marks
// the boundary, and the segment BELOW it runs in that FrRet's RetDef. Walking
// top-down with a def cursor rebinds every frame against its own def's AST.
func (in *Interp) rebindNodes(st *State) error {
	curDef := st.DefHash
	root, err := in.loadAST(curDef)
	if err != nil {
		return err
	}
	for i := len(st.Kont) - 1; i >= 0; i-- {
		f := st.Kont[i]
		if f.Kind == FrRet {
			curDef = f.RetDef
			if curDef != "" {
				root, err = in.loadAST(curDef)
				if err != nil {
					return err
				}
			}
			continue // FrRet navigates no children
		}
		n, ok := navigate(root, f.Path)
		if !ok {
			return errors.New("cek: cannot rebind frame node from path")
		}
		f.Node = n
	}
	return nil
}

func restartValue(choice RestartChoice) Value {
	r := newRecord()
	r.set("restart", strVal(choice.Name))
	for k, v := range choice.Args {
		r.set(k, anyToValue(v))
	}
	return recVal(r)
}

func grantedFuel(choice RestartChoice, fallback int64) int64 {
	if choice.Name == "grant-fuel" {
		if v, ok := choice.Args["fuel"]; ok {
			if n, ok2 := toInt64(v); ok2 {
				return n
			}
		}
	}
	if fallback > 0 {
		return fallback
	}
	return DefaultFuelSteps
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		return int64(x), true
	default:
		return 0, false
	}
}

func anyToValue(v any) Value {
	switch x := v.(type) {
	case nil:
		return null()
	case bool:
		return boolVal(x)
	case float64:
		return f64(x)
	case int:
		return f64(float64(x))
	case int64:
		return f64(float64(x))
	case string:
		return strVal(x)
	case *big.Int:
		return bigVal(x)
	default:
		return undef()
	}
}
