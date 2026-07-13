package cek

import "regel.dev/regel/internal/rast"

// FrameKind is the closed set of K frame kinds — one per admitted composite node
// kind (ADR-04 §2). The set is CLOSED and versioned with the CFR wire format:
// values are STABLE for CFR v1 and append-only. TryK/CatchK/FinallyK realize
// ADR-01's handler-frame stack inside K.
type FrameKind uint8

const (
	FrRet          FrameKind = iota // function activation boundary: catches Return, restores caller C/E
	FrBlock                         // execute a KList of statements; restore scope env on exit
	FrVarDecl                       // evaluate declarator initializers, bind pattern into a new record
	FrExprStmt                      // evaluate an expression statement, discard the value
	FrIf                            // cond evaluated → pick branch
	FrBin                           // binary op: left, right, apply
	FrLogic                         // short-circuit && || ??
	FrUnary                         // unary op
	FrUpdate                        // ++/-- (prefix/postfix) on an lvalue
	FrCond                          // ternary
	FrCall                          // eval callee + args, then apply the call
	FrMember                        // obj.prop
	FrIndex                         // obj[expr]
	FrArray                         // array literal elements
	FrObject                        // object literal properties
	FrReturn                        // return <expr> → Return signal
	FrThrow                         // throw <expr> → Throw signal
	FrCatch                         // catch handler (intercepts a Throw unwinding through it)
	FrFinally                       // finally runner (intercepts every signal, runs, then resumes)
	FrFor                           // C-style for(;;) driver
	FrForOf                         // for-of over an array driver
	FrWhile                         // while driver
	FrDoWhile                       // do-while driver
	FrSwitch                        // switch driver
	FrAssign                        // assignment / compound assignment target write
	FrAwait                         // await (Stage A: inline completion)
	FrTemplate                      // template literal parts
	FrTypeof                        // typeof operand
	FrMemberAssign                  // pending obj.prop = / obj[k] = compound member target
	FrFinallyRun                    // finally block executing; resumes the pending signal on completion
	frameKindMax
)

// Frame is the single uniform K frame (ADR-04 §2 {kind, node_path, vals[]}).
// The generic fields (Vals, Idx, Env) plus the composite Node (re-derivable from
// Path) carry every kind's progress; a handful of kinds use the aux fields.
type Frame struct {
	Kind FrameKind
	Node *rast.Node // live composite node; re-derived from Path + DefHash on decode
	Path Path       // node path of the composite node in DefHash
	Idx  int        // progress cursor (which child / iteration)
	Vals []Value    // sub-results accumulated so far
	Env  *Env       // env in which to evaluate remaining children

	// OuterEnv is the env to restore when a scope (block / loop / try) exits or
	// is unwound past.
	OuterEnv *Env

	// FrRet: restore the caller's control on return / unwind-crossing.
	RetDef  string
	RetPath Path
	RetEnv  *Env

	// Aux is a secondary cursor (call arg index, matched switch clause, …).
	Aux int
	// Obj / Key / IdxVal carry a resolved member/index assignment target, or the
	// object being built (FrObject), or the iterable (FrForOf).
	Obj    Value
	Key    string
	IdxVal Value
	// Pend is the signal a FrFinallyRun frame must resume once finally completes.
	Pend *Signal
}
