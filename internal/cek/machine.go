package cek

import (
	"context"
	"fmt"
	"time"

	"regel.dev/regel/internal/rast"
)

// Mode is the machine's dispatch mode at a transition boundary.
type Mode uint8

const (
	ModeEval   Mode = iota // evaluate m.node in m.env
	ModeApply              // deliver m.val to the top K frame
	ModeUnwind             // propagate m.sig through the K frames
)

type SigKind uint8

const (
	SigThrow SigKind = iota
	SigBreak
	SigContinue
	SigReturn
)

// Signal is a non-local control transfer propagating through K (ADR-01 exception
// semantics realized over frames): throw, break, continue, return.
type Signal struct {
	Kind SigKind
	Val  Value
}

// ParkKind records why a park happened, so resume knows how to re-enter.
type ParkKind uint8

const (
	ParkFuel     ParkKind = iota // fuelMeter breach: resume restores exactly + granted fuel
	ParkGovernor                 // governorMeter breach: resume restores exactly
	ParkSignal                   // std signal(): resume delivers restart value at the call point
	// APPEND-ONLY (ADR-05 §8: park kinds never renumber). ParkWake resumes a wake
	// park by delivering a value at the parked call point (timer: undefined;
	// message: payload; join: results). ParkFresh is an initial, never-stepped
	// state: resume delivers nothing and just runs (the CFR seed InitialState builds).
	ParkWake
	ParkFresh
)

// State is the complete serializable machine snapshot at a park point (ADR-05
// §2 C/E/K). It is what CFR encodes. Node is not stored — it is re-derived from
// DefHash + Path on decode, so the snapshot anchors only to immortal facts.
type State struct {
	DefHash string
	Path    Path
	Mode    Mode
	Val     Value
	Sig     Signal
	Env     *Env
	Kont    []*Frame // innermost last

	Tier      Tier
	FuelSteps int64
	FuelAlloc int64

	ParkKind ParkKind
}

// Principal is the resume/eval scope: the capability grants and operator status
// used to build the root environment and to gate restarts (ADR-04 §5, ADR-05 §6).
type Principal struct {
	Subject    string
	Grants     map[string]bool
	IsOperator bool
}

// RunReq configures a fresh evaluation.
type RunReq struct {
	DefHash    string
	Args       []Value
	Tier       Tier
	Fuel       int64         // sandbox step budget (0 → DefaultFuelSteps)
	Alloc      int64         // sandbox alloc budget (0 → DefaultFuelAllocBytes)
	GovCeiling int64         // trusted step ceiling (0 → DefaultGovernorCeiling)
	GovWall    time.Duration // trusted wall deadline (0 → DefaultGovernorWall)
	Principal  Principal
	RootEnv    *Env // optional capability root; nil → empty root
}

// machine is the live CEK state (ADR-04 §2). Registers C/E/K are (defHash/path/
// node + mode), env, kont.
type machine struct {
	in      *Interp
	defHash string
	root    *rast.Node // cached AST root of defHash
	node    *rast.Node // current node (eval mode)
	path    Path
	env     *Env
	kont    []*Frame

	mode Mode
	val  Value
	sig  Signal

	host        *Host
	tier        Tier
	transitions int64

	// meter budgets, snapshotted for resume.
	fuelSteps int64
	fuelAlloc int64
}

// Run evaluates a definition under the tier's meter. It dispatches to one of the
// two monomorphized loops, run[fuelMeter] / run[governorMeter] (ADR-04 §4).
func (in *Interp) Run(ctx context.Context, req RunReq) Outcome {
	m, err := in.newMachine(ctx, req)
	if err != nil {
		return Outcome{Kind: OutError, Err: err}
	}
	switch req.Tier {
	case TierTrusted:
		ceil := req.GovCeiling
		if ceil == 0 {
			ceil = DefaultGovernorCeiling
		}
		wall := req.GovWall
		if wall == 0 {
			wall = DefaultGovernorWall
		}
		gm := &governorMeter{ceiling: ceil, deadline: time.Now().Add(wall)}
		return run[*governorMeter](m, gm)
	default:
		steps := req.Fuel
		if steps == 0 {
			steps = DefaultFuelSteps
		}
		alloc := req.Alloc
		if alloc == 0 {
			alloc = DefaultFuelAllocBytes
		}
		m.fuelSteps = steps
		m.fuelAlloc = alloc
		fm := &fuelMeter{steps: steps, alloc: alloc}
		return run[*fuelMeter](m, fm)
	}
}

// newMachine builds the initial machine for an entry definition.
func (in *Interp) newMachine(ctx context.Context, req RunReq) (*machine, error) {
	root, err := in.loadAST(req.DefHash)
	if err != nil {
		return nil, err
	}
	host := &Host{ctx: ctx, reg: in.reg, Principal: req.Principal}
	m := &machine{
		in:      in,
		defHash: req.DefHash,
		root:    root,
		env:     req.RootEnv,
		host:    host,
		tier:    req.Tier,
	}
	// Bottom activation boundary: a top-level return / fall-off yields the result.
	m.kont = []*Frame{{Kind: FrRet, RetDef: "", RetEnv: nil}}

	switch root.Kind {
	case rast.KFunc:
		act, err := bindParams(root, req.Args, req.RootEnv)
		if err != nil {
			return nil, err
		}
		body := root.Kids[3]
		m.env = act
		m.setControl(body, Path{3})
	default:
		// A value definition (or bare expression): evaluate it directly.
		m.setControl(root, nil)
	}
	m.mode = ModeEval
	return m, nil
}

// setControl points C at a node with the given path (relative to the current
// definition root).
func (m *machine) setControl(n *rast.Node, p Path) {
	m.node = n
	m.path = p
	m.mode = ModeEval
}

// run is the generic step loop — the single machine, monomorphized per Meter
// (ADR-04 §4). The trusted instantiation's only per-step metering cost is the
// governor's 4096-counter check.
func run[M Meter](m *machine, meter M) Outcome {
	for {
		if !meter.tick() {
			return m.parkMeter(meter)
		}
		m.transitions++
		var o Outcome
		var done bool
		switch m.mode {
		case ModeEval:
			o, done = m.evalStep(meter)
		case ModeApply:
			o, done = m.applyStep(meter)
		case ModeUnwind:
			o, done = m.unwindStep()
		}
		if done {
			o.Transitions = m.transitions
			return o
		}
	}
}

// push/pop K --------------------------------------------------------------------

func (m *machine) push(f *Frame) { m.kont = append(m.kont, f) }

func (m *machine) top() *Frame { return m.kont[len(m.kont)-1] }

func (m *machine) pop() *Frame {
	f := m.kont[len(m.kont)-1]
	m.kont = m.kont[:len(m.kont)-1]
	return f
}

// apply transitions to Apply mode delivering v.
func (m *machine) apply(v Value) {
	m.mode = ModeApply
	m.val = v
}

// evalChild points C at child i of node n (path p is n's path) and stays in Eval.
func (m *machine) evalChild(n *rast.Node, p Path, i int) {
	m.node = n.Kids[i]
	m.path = p.child(i)
	m.mode = ModeEval
}

// raise starts unwinding with a signal.
func (m *machine) raise(k SigKind, v Value) {
	m.sig = Signal{Kind: k, Val: v}
	m.mode = ModeUnwind
}

// fault produces an internal error outcome (fail closed).
func (m *machine) fault(format string, args ...any) (Outcome, bool) {
	return Outcome{Kind: OutError, Err: fmt.Errorf("cek: "+format, args...)}, true
}

// parkMeter snapshots the full machine state on a meter breach and returns the
// durable condition (ADR-04 §4: exhaustion never panics).
func (m *machine) parkMeter(meter Meter) Outcome {
	pk := ParkFuel
	if meter.breachClass() == "runaway" {
		pk = ParkGovernor
	}
	st := m.snapshot(pk)
	return Outcome{
		Kind:  OutParked,
		State: st,
		Condition: &Condition{
			Class:    meter.breachClass(),
			Payload:  map[string]any{"def": m.defHash, "transitions": m.transitions},
			Restarts: meter.restarts(),
		},
		Transitions: m.transitions,
	}
}

// snapshot captures the serializable machine state (ADR-05 §2 C/E/K).
func (m *machine) snapshot(pk ParkKind) *State {
	return &State{
		DefHash:   m.defHash,
		Path:      m.path.clone(),
		Mode:      m.mode,
		Val:       m.val,
		Sig:       m.sig,
		Env:       m.env,
		Kont:      m.kont,
		Tier:      m.tier,
		FuelSteps: m.fuelSteps,
		FuelAlloc: m.fuelAlloc,
		ParkKind:  pk,
	}
}
