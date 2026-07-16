package cek

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"regel.dev/regel/internal/rast"
)

// RestartChoice is the operator/agent choice delivered to a parked machine
// (ADR-05 §6): the restart name plus its arguments.
type RestartChoice struct {
	Name string
	Args map[string]any
}

// Delivery is what a resume hands the parked machine (ADR-05 §5/§6). Exactly one
// arm is used per park kind: Restart carries the operator/agent choice for
// ParkSignal / ParkFuel / ParkGovernor parks; Value carries the delivered value
// for ParkWake parks (timer: undefined; message: payload; join: results).
// ParkFresh parks use neither.
type Delivery struct {
	Restart *RestartChoice // ParkSignal / ParkFuel / ParkGovernor
	Value   *Value         // ParkWake
}

// Resume re-enters a parked machine from a decoded State under a Principal (ADR-05
// §4: the resume host is rebuilt with the row's capabilities so a capability
// captured across the pause is re-validated). A fuel/governor park resumes at the
// exact suspended transition under a fresh budget (grant-fuel supplies it); a
// signal park delivers the restart value as the awaited value of the parked call;
// a wake park delivers d.Value at the parked call; a fresh state just runs.
func (in *Interp) Resume(ctx context.Context, st *State, d Delivery, p Principal) Outcome {
	if d.Restart != nil && d.Restart.Name == "abort" {
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
		host:      &Host{ctx: ctx, reg: in.reg, Principal: p, reader: in.reader},
		tier:      st.Tier,
		fuelSteps: st.FuelSteps,
		fuelAlloc: st.FuelAlloc,
	}

	switch st.ParkKind {
	case ParkSignal:
		// Deliver the restart value at the parked call point.
		if d.Restart != nil {
			m.val = restartValue(*d.Restart)
		} else {
			m.val = undef()
		}
		m.mode = ModeApply
	case ParkWake:
		// Deliver the wake value at the parked call point (undefined if none).
		if d.Value != nil {
			m.val = *d.Value
		} else {
			m.val = undef()
		}
		m.mode = ModeApply
	case ParkFresh:
		// A never-stepped state: mode/val are already the Eval-mode seed; deliver
		// nothing, just run.
	}

	switch st.Tier {
	case TierTrusted:
		gm := &governorMeter{ceiling: DefaultGovernorCeiling, deadline: time.Now().Add(DefaultGovernorWall)}
		return run[*governorMeter](m, gm)
	default:
		steps := grantedFuel(d.Restart, st.FuelSteps)
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

// InitialState builds the never-stepped State for invoking a definition (or a
// closure value) with args under a tier/budget — the CFR seed for a fresh
// workflow row or a join child. ParkKind = ParkFresh. clo nil → top-level
// definition by hash (same param binding as newMachine); clo non-nil → the
// closure applied to args. The bottom FrRet frame mirrors newMachine.
func (in *Interp) InitialState(defHash string, clo *ClosureObj, args []Value, tier Tier, fuel, alloc int64) (*State, error) {
	kont := []*Frame{{Kind: FrRet, RetDef: "", RetEnv: nil}}
	st := &State{
		Mode:      ModeEval,
		Kont:      kont,
		Tier:      tier,
		FuelSteps: fuel,
		FuelAlloc: alloc,
		ParkKind:  ParkFresh,
	}
	if clo != nil {
		root, err := in.loadAST(clo.DefHash)
		if err != nil {
			return nil, err
		}
		fnNode, ok := navigate(root, clo.Path)
		if !ok {
			return nil, fmt.Errorf("cek: InitialState cannot navigate closure path in %s", clo.DefHash)
		}
		if fnNode.Kind != rast.KFunc {
			return nil, fmt.Errorf("cek: InitialState closure is not a function (kind %d)", fnNode.Kind)
		}
		act, err := bindParams(fnNode, args, clo.Env)
		if err != nil {
			return nil, err
		}
		st.DefHash = clo.DefHash
		st.Env = act
		st.Path = clo.Path.child(3)
		return st, nil
	}
	root, err := in.loadAST(defHash)
	if err != nil {
		return nil, err
	}
	st.DefHash = defHash
	switch root.Kind {
	case rast.KFunc:
		act, err := bindParams(root, args, nil)
		if err != nil {
			return nil, err
		}
		st.Env = act
		st.Path = Path{3}
	default:
		st.Env = nil
		st.Path = nil
	}
	return st, nil
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

func grantedFuel(choice *RestartChoice, fallback int64) int64 {
	if choice != nil && choice.Name == "grant-fuel" {
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
