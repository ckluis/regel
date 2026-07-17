// Package m5eval is the Stage-E BUILD-E real-LLM eval corpus that flips the OPEN
// M5 gates from ADR-12: §3a authoring pass@k, §7 restart-decision accuracy, §5
// eval-derived fuel capacity. Every metric it reports comes from a captured real
// `claude -p` run driving the real MCP agent plane — nothing here fakes a number.
// The corpus is DATA (this file); the harness (m5_test.go) drives it; the pins
// (pin.go) freeze k + the corpus hash per epoch (the REVIEW-PRE-E §4 L2 fix).
package m5eval

// --- §3a authoring corpus ----------------------------------------------------

// AuthoringTask is one natural-language authoring task with a machine-checkable
// admissible target. The agent is given Spec (+ the required Module/Entry/
// Signature) and must produce regel-TS that (a) is ADMITTED by the real ADR-07
// pipeline via patch.submit{commit:true} and (b) is BEHAVIORALLY CORRECT — its
// output on every Input matches the Reference solution's output. Admission alone
// does not pass a task; the per-task oracle must also agree (red-path: a
// known-bad-but-admissible solution FAILS). Reference is the known-good witness,
// validated in the seed test to admit and to satisfy every Input.
type AuthoringTask struct {
	ID        string
	Spec      string  // the natural-language brief handed to the LLM
	Module    string  // catalog module, e.g. "app/agent/factorial"
	Entry     string  // exported function name
	Signature string  // the required signature, e.g. "factorial(n: number): number"
	Reference string  // known-good solution (ground-truth oracle)
	KnownBad  string  // admissible but WRONG (harness self-test: must FAIL the oracle)
	Inputs    [][]any // input vectors; each element float64 | string | bool
}

// AuthoringCorpus is the versioned authoring suite. Monotone (ADR-12 §3a): a task
// once added is never silently dropped. Scalar in/out only (the reference reducer
// arg builder covers number|string|bool), pure compute (no std imports) so the
// oracle needs no intrinsics. Grow N toward the ADR §3a floor (N≥50) per epoch.
var AuthoringCorpus = []AuthoringTask{
	{
		ID: "add_two", Module: "app/agent/add_two", Entry: "addTwo",
		Signature: "addTwo(a: number, b: number): number",
		Spec:      "Write a function addTwo(a, b) that returns the sum of its two number arguments.",
		Reference: "export function addTwo(a: number, b: number): number {\n  return a + b;\n}\n",
		KnownBad:  "export function addTwo(a: number, b: number): number {\n  return a - b;\n}\n",
		Inputs:    [][]any{{2.0, 3.0}, {-1.0, 1.0}, {10.0, 0.0}, {-4.0, -6.0}},
	},
	{
		ID: "abs_val", Module: "app/agent/abs_val", Entry: "absVal",
		Signature: "absVal(x: number): number",
		Spec:      "Write absVal(x) that returns the absolute value of x (its magnitude, always non-negative).",
		Reference: "export function absVal(x: number): number {\n  if (x < 0) { return -x; }\n  return x;\n}\n",
		KnownBad:  "export function absVal(x: number): number {\n  return x;\n}\n",
		Inputs:    [][]any{{-5.0}, {7.0}, {0.0}, {-0.5}},
	},
	{
		ID: "max_two", Module: "app/agent/max_two", Entry: "maxTwo",
		Signature: "maxTwo(a: number, b: number): number",
		Spec:      "Write maxTwo(a, b) that returns the larger of the two numbers (return either if equal).",
		Reference: "export function maxTwo(a: number, b: number): number {\n  if (a > b) { return a; }\n  return b;\n}\n",
		KnownBad:  "export function maxTwo(a: number, b: number): number {\n  if (a < b) { return a; }\n  return b;\n}\n",
		Inputs:    [][]any{{3.0, 9.0}, {9.0, 3.0}, {4.0, 4.0}, {-2.0, -8.0}},
	},
	{
		ID: "factorial", Module: "app/agent/factorial", Entry: "factorial",
		Signature: "factorial(n: number): number",
		Spec:      "Write factorial(n) computing n! iteratively (no recursion): factorial(0) is 1, factorial(5) is 120.",
		Reference: "export function factorial(n: number): number {\n  let acc = 1;\n  let i = 2;\n  while (i <= n) {\n    acc = acc * i;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function factorial(n: number): number {\n  let acc = 1;\n  let i = 2;\n  while (i < n) {\n    acc = acc * i;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		Inputs:    [][]any{{0.0}, {1.0}, {3.0}, {5.0}, {6.0}},
	},
	{
		ID: "sum_to_n", Module: "app/agent/sum_to_n", Entry: "sumToN",
		Signature: "sumToN(n: number): number",
		Spec:      "Write sumToN(n) returning the sum of all integers from 1 to n inclusive (sumToN(5) is 15). Return 0 for n < 1.",
		Reference: "export function sumToN(n: number): number {\n  let acc = 0;\n  let i = 1;\n  while (i <= n) {\n    acc = acc + i;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function sumToN(n: number): number {\n  return n * n;\n}\n",
		Inputs:    [][]any{{1.0}, {5.0}, {10.0}, {0.0}},
	},
	{
		ID: "is_even", Module: "app/agent/is_even", Entry: "isEven",
		Signature: "isEven(n: number): boolean",
		Spec:      "Write isEven(n) returning true when the integer n is even, false otherwise.",
		Reference: "export function isEven(n: number): boolean {\n  return n % 2 === 0;\n}\n",
		KnownBad:  "export function isEven(n: number): boolean {\n  return n % 2 === 1;\n}\n",
		Inputs:    [][]any{{4.0}, {7.0}, {0.0}, {-3.0}, {-8.0}},
	},
	{
		ID: "fib", Module: "app/agent/fib", Entry: "fib",
		Signature: "fib(n: number): number",
		Spec:      "Write fib(n) returning the nth Fibonacci number iteratively, 0-indexed: fib(0)=0, fib(1)=1, fib(7)=13.",
		Reference: "export function fib(n: number): number {\n  let a = 0;\n  let b = 1;\n  let i = 0;\n  while (i < n) {\n    const t = a + b;\n    a = b;\n    b = t;\n    i = i + 1;\n  }\n  return a;\n}\n",
		KnownBad:  "export function fib(n: number): number {\n  let a = 0;\n  let b = 1;\n  let i = 0;\n  while (i < n) {\n    const t = a + b;\n    a = b;\n    b = t;\n    i = i + 1;\n  }\n  return b;\n}\n",
		Inputs:    [][]any{{0.0}, {1.0}, {2.0}, {7.0}, {10.0}},
	},
	{
		ID: "gcd", Module: "app/agent/gcd", Entry: "gcd",
		Signature: "gcd(a: number, b: number): number",
		Spec:      "Write gcd(a, b) returning the greatest common divisor of two positive integers using the Euclidean algorithm.",
		Reference: "export function gcd(a: number, b: number): number {\n  let x = a;\n  let y = b;\n  while (y !== 0) {\n    const t = x % y;\n    x = y;\n    y = t;\n  }\n  return x;\n}\n",
		KnownBad:  "export function gcd(a: number, b: number): number {\n  return a % b;\n}\n",
		Inputs:    [][]any{{12.0, 8.0}, {17.0, 5.0}, {9.0, 3.0}, {48.0, 36.0}},
	},
	{
		ID: "power", Module: "app/agent/power", Entry: "power",
		Signature: "power(base: number, exp: number): number",
		Spec:      "Write power(base, exp) returning base raised to a non-negative integer exponent exp, iteratively. power(2,10)=1024, power(5,0)=1.",
		Reference: "export function power(base: number, exp: number): number {\n  let acc = 1;\n  let i = 0;\n  while (i < exp) {\n    acc = acc * base;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function power(base: number, exp: number): number {\n  return base * exp;\n}\n",
		Inputs:    [][]any{{2.0, 10.0}, {3.0, 3.0}, {5.0, 0.0}, {7.0, 2.0}},
	},
	{
		ID: "clamp", Module: "app/agent/clamp", Entry: "clamp",
		Signature: "clamp(x: number, lo: number, hi: number): number",
		Spec:      "Write clamp(x, lo, hi) returning x constrained to the range [lo, hi]: below lo returns lo, above hi returns hi, else x.",
		Reference: "export function clamp(x: number, lo: number, hi: number): number {\n  if (x < lo) { return lo; }\n  if (x > hi) { return hi; }\n  return x;\n}\n",
		KnownBad:  "export function clamp(x: number, lo: number, hi: number): number {\n  if (x < lo) { return hi; }\n  if (x > hi) { return lo; }\n  return x;\n}\n",
		Inputs:    [][]any{{5.0, 0.0, 10.0}, {-3.0, 0.0, 10.0}, {15.0, 0.0, 10.0}, {7.0, 7.0, 7.0}},
	},
	{
		ID: "digit_sum", Module: "app/agent/digit_sum", Entry: "digitSum",
		Signature: "digitSum(n: number): number",
		Spec:      "Write digitSum(n) returning the sum of the decimal digits of a non-negative integer n. digitSum(123)=6, digitSum(0)=0.",
		Reference: "export function digitSum(n: number): number {\n  let x = n;\n  let acc = 0;\n  while (x > 0) {\n    acc = acc + (x % 10);\n    x = (x - (x % 10)) / 10;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function digitSum(n: number): number {\n  return n % 10;\n}\n",
		Inputs:    [][]any{{123.0}, {0.0}, {999.0}, {4560.0}},
	},
	{
		ID: "is_prime", Module: "app/agent/is_prime", Entry: "isPrime",
		Signature: "isPrime(n: number): boolean",
		Spec:      "Write isPrime(n) returning true when the integer n is a prime number (n<2 is not prime). Check divisibility by a trial loop.",
		Reference: "export function isPrime(n: number): boolean {\n  if (n < 2) { return false; }\n  let i = 2;\n  while (i < n) {\n    if (n % i === 0) { return false; }\n    i = i + 1;\n  }\n  return true;\n}\n",
		KnownBad:  "export function isPrime(n: number): boolean {\n  if (n < 2) { return false; }\n  return n % 2 !== 0;\n}\n",
		Inputs:    [][]any{{2.0}, {4.0}, {13.0}, {1.0}, {15.0}, {17.0}},
	},
	{
		ID: "celsius_to_f", Module: "app/agent/celsius_to_f", Entry: "cToF",
		Signature: "cToF(c: number): number",
		Spec:      "Write cToF(c) converting a Celsius temperature to Fahrenheit: F = c * 9 / 5 + 32. cToF(100)=212, cToF(-40)=-40.",
		Reference: "export function cToF(c: number): number {\n  return c * 9 / 5 + 32;\n}\n",
		KnownBad:  "export function cToF(c: number): number {\n  return c * 9 / 5;\n}\n",
		Inputs:    [][]any{{0.0}, {100.0}, {-40.0}, {37.0}},
	},
	{
		ID: "greet_concat", Module: "app/agent/greet_concat", Entry: "greet",
		Signature: "greet(name: string): string",
		Spec:      "Write greet(name) returning the string \"hello, \" followed by name and a trailing \"!\". greet(\"bob\") is \"hello, bob!\".",
		Reference: "export function greet(name: string): string {\n  return \"hello, \" + name + \"!\";\n}\n",
		KnownBad:  "export function greet(name: string): string {\n  return \"hi, \" + name;\n}\n",
		Inputs:    [][]any{{"bob"}, {"Ada"}, {""}, {"world"}},
	},
	{
		ID: "stars", Module: "app/agent/stars", Entry: "stars",
		Signature: "stars(n: number): string",
		Spec:      "Write stars(n) returning a string of exactly n asterisk characters ('*'). stars(3) is \"***\", stars(0) is \"\".",
		Reference: "export function stars(n: number): string {\n  let s = \"\";\n  let i = 0;\n  while (i < n) {\n    s = s + \"*\";\n    i = i + 1;\n  }\n  return s;\n}\n",
		KnownBad:  "export function stars(n: number): string {\n  let s = \"\";\n  let i = 0;\n  while (i <= n) {\n    s = s + \"*\";\n    i = i + 1;\n  }\n  return s;\n}\n",
		Inputs:    [][]any{{3.0}, {0.0}, {1.0}, {5.0}},
	},

	// --- BUILD-E R5 expansion (run 3): tasks 16-52, lifting N past the ADR-12
	// §3a floor N≥50. Same closed-dialect family as tasks 1-15 (scalar in/out,
	// pure compute, while-loop iteration); every Reference/KnownBad is oracle-
	// validated by TestOracleDiscriminates before any LLM call is spent.
	{
		ID: "min_two", Module: "app/agent/min_two", Entry: "minTwo",
		Signature: "minTwo(a: number, b: number): number",
		Spec:      "Write minTwo(a, b) that returns the smaller of the two numbers (return either if equal).",
		Reference: "export function minTwo(a: number, b: number): number {\n  if (a < b) { return a; }\n  return b;\n}\n",
		KnownBad:  "export function minTwo(a: number, b: number): number {\n  if (a > b) { return a; }\n  return b;\n}\n",
		Inputs:    [][]any{{3.0, 9.0}, {9.0, 3.0}, {4.0, 4.0}, {-2.0, -8.0}},
	},
	{
		ID: "max_three", Module: "app/agent/max_three", Entry: "maxThree",
		Signature: "maxThree(a: number, b: number, c: number): number",
		Spec:      "Write maxThree(a, b, c) that returns the largest of the three numbers.",
		Reference: "export function maxThree(a: number, b: number, c: number): number {\n  let m = a;\n  if (b > m) { m = b; }\n  if (c > m) { m = c; }\n  return m;\n}\n",
		KnownBad:  "export function maxThree(a: number, b: number, c: number): number {\n  let m = a;\n  if (b > m) { m = b; }\n  if (c > a) { m = c; }\n  return m;\n}\n",
		Inputs:    [][]any{{1.0, 5.0, 3.0}, {7.0, 2.0, 4.0}, {2.0, 9.0, 3.0}, {6.0, 6.0, 6.0}},
	},
	{
		ID: "min_three", Module: "app/agent/min_three", Entry: "minThree",
		Signature: "minThree(a: number, b: number, c: number): number",
		Spec:      "Write minThree(a, b, c) that returns the smallest of the three numbers.",
		Reference: "export function minThree(a: number, b: number, c: number): number {\n  let m = a;\n  if (b < m) { m = b; }\n  if (c < m) { m = c; }\n  return m;\n}\n",
		KnownBad:  "export function minThree(a: number, b: number, c: number): number {\n  let m = a;\n  if (b < m) { m = b; }\n  return m;\n}\n",
		Inputs:    [][]any{{3.0, 1.0, 2.0}, {5.0, 5.0, 5.0}, {9.0, 8.0, 1.0}, {2.0, 9.0, 4.0}},
	},
	{
		ID: "sign", Module: "app/agent/sign", Entry: "sign",
		Signature: "sign(x: number): number",
		Spec:      "Write sign(x) returning 1 when x is positive, -1 when negative, 0 when zero.",
		Reference: "export function sign(x: number): number {\n  if (x > 0) { return 1; }\n  if (x < 0) { return -1; }\n  return 0;\n}\n",
		KnownBad:  "export function sign(x: number): number {\n  if (x > 0) { return 1; }\n  return -1;\n}\n",
		Inputs:    [][]any{{-5.0}, {0.0}, {7.0}, {-0.5}},
	},
	{
		ID: "is_odd", Module: "app/agent/is_odd", Entry: "isOdd",
		Signature: "isOdd(n: number): boolean",
		Spec:      "Write isOdd(n) returning true when the integer n is odd, false otherwise (works for negative integers too).",
		Reference: "export function isOdd(n: number): boolean {\n  return n % 2 !== 0;\n}\n",
		KnownBad:  "export function isOdd(n: number): boolean {\n  return n % 2 === 1;\n}\n",
		Inputs:    [][]any{{3.0}, {4.0}, {0.0}, {-3.0}, {-8.0}},
	},
	{
		ID: "abs_diff", Module: "app/agent/abs_diff", Entry: "absDiff",
		Signature: "absDiff(a: number, b: number): number",
		Spec:      "Write absDiff(a, b) returning the absolute difference |a - b| (always non-negative).",
		Reference: "export function absDiff(a: number, b: number): number {\n  if (a > b) { return a - b; }\n  return b - a;\n}\n",
		KnownBad:  "export function absDiff(a: number, b: number): number {\n  return a - b;\n}\n",
		Inputs:    [][]any{{5.0, 3.0}, {3.0, 5.0}, {-1.0, 4.0}, {2.0, 2.0}},
	},
	{
		ID: "avg_two", Module: "app/agent/avg_two", Entry: "avgTwo",
		Signature: "avgTwo(a: number, b: number): number",
		Spec:      "Write avgTwo(a, b) returning the arithmetic mean of the two numbers: (a + b) / 2.",
		Reference: "export function avgTwo(a: number, b: number): number {\n  return (a + b) / 2;\n}\n",
		KnownBad:  "export function avgTwo(a: number, b: number): number {\n  return a + b / 2;\n}\n",
		Inputs:    [][]any{{3.0, 4.0}, {0.0, 0.0}, {-2.0, 2.0}, {10.0, 5.0}},
	},
	{
		ID: "cube", Module: "app/agent/cube", Entry: "cube",
		Signature: "cube(x: number): number",
		Spec:      "Write cube(x) returning x raised to the third power (x * x * x).",
		Reference: "export function cube(x: number): number {\n  return x * x * x;\n}\n",
		KnownBad:  "export function cube(x: number): number {\n  return x * x;\n}\n",
		Inputs:    [][]any{{2.0}, {-3.0}, {0.0}, {5.0}},
	},
	{
		ID: "hypot_sq", Module: "app/agent/hypot_sq", Entry: "hypotSq",
		Signature: "hypotSq(a: number, b: number): number",
		Spec:      "Write hypotSq(a, b) returning the sum of the squares of the two numbers: a*a + b*b.",
		Reference: "export function hypotSq(a: number, b: number): number {\n  return a * a + b * b;\n}\n",
		KnownBad:  "export function hypotSq(a: number, b: number): number {\n  return (a + b) * (a + b);\n}\n",
		Inputs:    [][]any{{3.0, 4.0}, {0.0, 5.0}, {1.0, 1.0}, {6.0, 8.0}},
	},
	{
		ID: "count_digits", Module: "app/agent/count_digits", Entry: "countDigits",
		Signature: "countDigits(n: number): number",
		Spec:      "Write countDigits(n) returning how many decimal digits the non-negative integer n has. countDigits(0) is 1, countDigits(123) is 3.",
		Reference: "export function countDigits(n: number): number {\n  if (n === 0) { return 1; }\n  let x = n;\n  let c = 0;\n  while (x > 0) {\n    c = c + 1;\n    x = (x - (x % 10)) / 10;\n  }\n  return c;\n}\n",
		KnownBad:  "export function countDigits(n: number): number {\n  let x = n;\n  let c = 0;\n  while (x > 0) {\n    c = c + 1;\n    x = (x - (x % 10)) / 10;\n  }\n  return c;\n}\n",
		Inputs:    [][]any{{0.0}, {5.0}, {123.0}, {4567.0}},
	},
	{
		ID: "reverse_num", Module: "app/agent/reverse_num", Entry: "reverseNum",
		Signature: "reverseNum(n: number): number",
		Spec:      "Write reverseNum(n) returning the non-negative integer n with its decimal digits reversed. reverseNum(123) is 321; trailing zeros drop, so reverseNum(120) is 21.",
		Reference: "export function reverseNum(n: number): number {\n  let x = n;\n  let acc = 0;\n  while (x > 0) {\n    acc = acc * 10 + (x % 10);\n    x = (x - (x % 10)) / 10;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function reverseNum(n: number): number {\n  return n;\n}\n",
		Inputs:    [][]any{{123.0}, {120.0}, {7.0}, {0.0}, {4005.0}},
	},
	{
		ID: "is_palindrome_num", Module: "app/agent/is_palindrome_num", Entry: "isPalindromeNum",
		Signature: "isPalindromeNum(n: number): boolean",
		Spec:      "Write isPalindromeNum(n) returning true when the non-negative integer n reads the same forwards and backwards in decimal. 121 and 7 are palindromes; 123 and 10 are not.",
		Reference: "export function isPalindromeNum(n: number): boolean {\n  let x = n;\n  let r = 0;\n  while (x > 0) {\n    r = r * 10 + (x % 10);\n    x = (x - (x % 10)) / 10;\n  }\n  return r === n;\n}\n",
		KnownBad:  "export function isPalindromeNum(n: number): boolean {\n  let x = n;\n  let r = 0;\n  while (x > 0) {\n    r = r * 10 + (x % 10);\n    x = (x - (x % 10)) / 10;\n  }\n  return r >= n;\n}\n",
		Inputs:    [][]any{{121.0}, {123.0}, {7.0}, {1221.0}, {10.0}},
	},
	{
		ID: "digit_product", Module: "app/agent/digit_product", Entry: "digitProduct",
		Signature: "digitProduct(n: number): number",
		Spec:      "Write digitProduct(n) returning the product of the decimal digits of the positive integer n. digitProduct(29) is 18.",
		Reference: "export function digitProduct(n: number): number {\n  let x = n;\n  let acc = 1;\n  while (x > 0) {\n    acc = acc * (x % 10);\n    x = (x - (x % 10)) / 10;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function digitProduct(n: number): number {\n  let x = n;\n  let acc = 0;\n  while (x > 0) {\n    acc = acc + (x % 10);\n    x = (x - (x % 10)) / 10;\n  }\n  return acc;\n}\n",
		Inputs:    [][]any{{29.0}, {999.0}, {45.0}, {7.0}},
	},
	{
		ID: "sum_even_to_n", Module: "app/agent/sum_even_to_n", Entry: "sumEvenToN",
		Signature: "sumEvenToN(n: number): number",
		Spec:      "Write sumEvenToN(n) returning the sum of all even integers from 1 to n inclusive. sumEvenToN(6) is 12. Return 0 for n < 2.",
		Reference: "export function sumEvenToN(n: number): number {\n  let acc = 0;\n  let i = 2;\n  while (i <= n) {\n    acc = acc + i;\n    i = i + 2;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function sumEvenToN(n: number): number {\n  let acc = 0;\n  let i = 1;\n  while (i <= n) {\n    acc = acc + i;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		Inputs:    [][]any{{6.0}, {7.0}, {1.0}, {10.0}},
	},
	{
		ID: "sum_odd_to_n", Module: "app/agent/sum_odd_to_n", Entry: "sumOddToN",
		Signature: "sumOddToN(n: number): number",
		Spec:      "Write sumOddToN(n) returning the sum of all odd integers from 1 to n inclusive. sumOddToN(5) is 9. Return 0 for n < 1.",
		Reference: "export function sumOddToN(n: number): number {\n  let acc = 0;\n  let i = 1;\n  while (i <= n) {\n    acc = acc + i;\n    i = i + 2;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function sumOddToN(n: number): number {\n  let acc = 0;\n  let i = 2;\n  while (i <= n) {\n    acc = acc + i;\n    i = i + 2;\n  }\n  return acc;\n}\n",
		Inputs:    [][]any{{5.0}, {10.0}, {1.0}, {0.0}},
	},
	{
		ID: "sum_range", Module: "app/agent/sum_range", Entry: "sumRange",
		Signature: "sumRange(a: number, b: number): number",
		Spec:      "Write sumRange(a, b) returning the sum of all integers from a to b inclusive (a <= b). sumRange(1, 5) is 15.",
		Reference: "export function sumRange(a: number, b: number): number {\n  let acc = 0;\n  let i = a;\n  while (i <= b) {\n    acc = acc + i;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function sumRange(a: number, b: number): number {\n  let acc = 0;\n  let i = a;\n  while (i < b) {\n    acc = acc + i;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		Inputs:    [][]any{{1.0, 5.0}, {3.0, 3.0}, {0.0, 4.0}, {2.0, 6.0}},
	},
	{
		ID: "sum_squares_to_n", Module: "app/agent/sum_squares_to_n", Entry: "sumSquaresToN",
		Signature: "sumSquaresToN(n: number): number",
		Spec:      "Write sumSquaresToN(n) returning 1*1 + 2*2 + ... + n*n. sumSquaresToN(3) is 14. Return 0 for n < 1.",
		Reference: "export function sumSquaresToN(n: number): number {\n  let acc = 0;\n  let i = 1;\n  while (i <= n) {\n    acc = acc + i * i;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function sumSquaresToN(n: number): number {\n  return n * n;\n}\n",
		Inputs:    [][]any{{3.0}, {1.0}, {0.0}, {5.0}},
	},
	{
		ID: "is_divisible", Module: "app/agent/is_divisible", Entry: "isDivisible",
		Signature: "isDivisible(a: number, b: number): boolean",
		Spec:      "Write isDivisible(a, b) returning true when the integer a is evenly divisible by the non-zero integer b.",
		Reference: "export function isDivisible(a: number, b: number): boolean {\n  return a % b === 0;\n}\n",
		KnownBad:  "export function isDivisible(a: number, b: number): boolean {\n  return b % a === 0;\n}\n",
		Inputs:    [][]any{{6.0, 3.0}, {7.0, 2.0}, {9.0, 9.0}, {10.0, 4.0}},
	},
	{
		ID: "next_multiple", Module: "app/agent/next_multiple", Entry: "nextMultiple",
		Signature: "nextMultiple(n: number, d: number): number",
		Spec:      "Write nextMultiple(n, d) returning the smallest multiple of the positive integer d that is greater than or equal to the non-negative integer n. nextMultiple(7, 3) is 9; nextMultiple(9, 3) is 9.",
		Reference: "export function nextMultiple(n: number, d: number): number {\n  const r = n % d;\n  if (r === 0) { return n; }\n  return n + d - r;\n}\n",
		KnownBad:  "export function nextMultiple(n: number, d: number): number {\n  return n + d - (n % d);\n}\n",
		Inputs:    [][]any{{7.0, 3.0}, {9.0, 3.0}, {1.0, 5.0}, {0.0, 4.0}},
	},
	{
		ID: "nearest_ten", Module: "app/agent/nearest_ten", Entry: "nearestTen",
		Signature: "nearestTen(n: number): number",
		Spec:      "Write nearestTen(n) rounding the non-negative integer n to the nearest multiple of 10; exact halves (remainder 5) round up. nearestTen(12) is 10, nearestTen(15) is 20.",
		Reference: "export function nearestTen(n: number): number {\n  const r = n % 10;\n  if (r < 5) { return n - r; }\n  return n + 10 - r;\n}\n",
		KnownBad:  "export function nearestTen(n: number): number {\n  return n - (n % 10);\n}\n",
		Inputs:    [][]any{{12.0}, {15.0}, {7.0}, {20.0}, {4.0}},
	},
	{
		ID: "count_divisors", Module: "app/agent/count_divisors", Entry: "countDivisors",
		Signature: "countDivisors(n: number): number",
		Spec:      "Write countDivisors(n) returning how many positive integers from 1 to n divide the positive integer n evenly. countDivisors(6) is 4 (1, 2, 3, 6).",
		Reference: "export function countDivisors(n: number): number {\n  let c = 0;\n  let i = 1;\n  while (i <= n) {\n    if (n % i === 0) { c = c + 1; }\n    i = i + 1;\n  }\n  return c;\n}\n",
		KnownBad:  "export function countDivisors(n: number): number {\n  let c = 0;\n  let i = 2;\n  while (i <= n) {\n    if (n % i === 0) { c = c + 1; }\n    i = i + 1;\n  }\n  return c;\n}\n",
		Inputs:    [][]any{{1.0}, {6.0}, {12.0}, {13.0}},
	},
	{
		ID: "collatz_steps", Module: "app/agent/collatz_steps", Entry: "collatzSteps",
		Signature: "collatzSteps(n: number): number",
		Spec:      "Write collatzSteps(n) returning how many steps the Collatz process takes to reach 1 from the positive integer n (halve when even, else 3n+1). collatzSteps(1) is 0, collatzSteps(6) is 8.",
		Reference: "export function collatzSteps(n: number): number {\n  let x = n;\n  let steps = 0;\n  while (x !== 1) {\n    if (x % 2 === 0) {\n      x = x / 2;\n    } else {\n      x = 3 * x + 1;\n    }\n    steps = steps + 1;\n  }\n  return steps;\n}\n",
		KnownBad:  "export function collatzSteps(n: number): number {\n  let x = n;\n  let steps = 1;\n  while (x !== 1) {\n    if (x % 2 === 0) {\n      x = x / 2;\n    } else {\n      x = 3 * x + 1;\n    }\n    steps = steps + 1;\n  }\n  return steps;\n}\n",
		Inputs:    [][]any{{1.0}, {6.0}, {7.0}, {27.0}},
	},
	{
		ID: "is_perfect_square", Module: "app/agent/is_perfect_square", Entry: "isPerfectSquare",
		Signature: "isPerfectSquare(n: number): boolean",
		Spec:      "Write isPerfectSquare(n) returning true when the non-negative integer n is a perfect square (0, 1, 4, 9, 16, ...). Use a trial loop, no square-root call.",
		Reference: "export function isPerfectSquare(n: number): boolean {\n  let i = 0;\n  while (i * i < n) {\n    i = i + 1;\n  }\n  return i * i === n;\n}\n",
		KnownBad:  "export function isPerfectSquare(n: number): boolean {\n  let i = 0;\n  while (i * i <= n) {\n    i = i + 1;\n  }\n  return i * i === n;\n}\n",
		Inputs:    [][]any{{0.0}, {9.0}, {8.0}, {16.0}, {15.0}},
	},
	{
		ID: "leap_year", Module: "app/agent/leap_year", Entry: "isLeapYear",
		Signature: "isLeapYear(y: number): boolean",
		Spec:      "Write isLeapYear(y) returning true for Gregorian leap years: divisible by 4, except centuries, which must be divisible by 400. 2000 and 2024 are leap years; 1900 and 2023 are not.",
		Reference: "export function isLeapYear(y: number): boolean {\n  return (y % 4 === 0 && y % 100 !== 0) || y % 400 === 0;\n}\n",
		KnownBad:  "export function isLeapYear(y: number): boolean {\n  return y % 4 === 0;\n}\n",
		Inputs:    [][]any{{2000.0}, {1900.0}, {2024.0}, {2023.0}, {2100.0}},
	},
	{
		ID: "pow_mod", Module: "app/agent/pow_mod", Entry: "powMod",
		Signature: "powMod(base: number, exp: number, m: number): number",
		Spec:      "Write powMod(base, exp, m) returning (base to the power exp) modulo m, for a non-negative integer exp and a modulus m > 1. Keep the accumulator reduced modulo m inside the loop. powMod(2, 10, 1000) is 24.",
		Reference: "export function powMod(base: number, exp: number, m: number): number {\n  let acc = 1;\n  let i = 0;\n  while (i < exp) {\n    acc = (acc * base) % m;\n    i = i + 1;\n  }\n  return acc;\n}\n",
		KnownBad:  "export function powMod(base: number, exp: number, m: number): number {\n  return (base * exp) % m;\n}\n",
		Inputs:    [][]any{{2.0, 10.0, 1000.0}, {3.0, 3.0, 5.0}, {5.0, 0.0, 7.0}, {7.0, 4.0, 10.0}},
	},
	{
		ID: "lcm", Module: "app/agent/lcm", Entry: "lcm",
		Signature: "lcm(a: number, b: number): number",
		Spec:      "Write lcm(a, b) returning the least common multiple of two positive integers. lcm(4, 6) is 12.",
		Reference: "export function lcm(a: number, b: number): number {\n  let m = a;\n  while (m % b !== 0) {\n    m = m + a;\n  }\n  return m;\n}\n",
		KnownBad:  "export function lcm(a: number, b: number): number {\n  return a * b;\n}\n",
		Inputs:    [][]any{{4.0, 6.0}, {3.0, 5.0}, {7.0, 7.0}, {2.0, 8.0}},
	},
	{
		ID: "floor_div", Module: "app/agent/floor_div", Entry: "floorDiv",
		Signature: "floorDiv(a: number, b: number): number",
		Spec:      "Write floorDiv(a, b) returning the integer quotient of the non-negative integer a divided by the positive integer b, discarding the remainder. floorDiv(7, 2) is 3.",
		Reference: "export function floorDiv(a: number, b: number): number {\n  return (a - (a % b)) / b;\n}\n",
		KnownBad:  "export function floorDiv(a: number, b: number): number {\n  return a / b;\n}\n",
		Inputs:    [][]any{{7.0, 2.0}, {10.0, 5.0}, {9.0, 4.0}, {1.0, 3.0}},
	},
	{
		ID: "century", Module: "app/agent/century", Entry: "century",
		Signature: "century(y: number): number",
		Spec:      "Write century(y) returning the century a positive year belongs to: years 1-100 are century 1, 101-200 are century 2. century(2026) is 21.",
		Reference: "export function century(y: number): number {\n  return (y + 99 - ((y + 99) % 100)) / 100;\n}\n",
		KnownBad:  "export function century(y: number): number {\n  return (y - (y % 100)) / 100;\n}\n",
		Inputs:    [][]any{{1.0}, {100.0}, {101.0}, {2000.0}, {2026.0}},
	},
	{
		ID: "f_to_c", Module: "app/agent/f_to_c", Entry: "fToC",
		Signature: "fToC(f: number): number",
		Spec:      "Write fToC(f) converting a Fahrenheit temperature to Celsius: C = (f - 32) * 5 / 9. fToC(212) is 100, fToC(-40) is -40.",
		Reference: "export function fToC(f: number): number {\n  return (f - 32) * 5 / 9;\n}\n",
		KnownBad:  "export function fToC(f: number): number {\n  return f * 5 / 9 - 32;\n}\n",
		Inputs:    [][]any{{212.0}, {32.0}, {-40.0}, {41.0}},
	},
	{
		ID: "fizzbuzz_str", Module: "app/agent/fizzbuzz_str", Entry: "fizzbuzz",
		Signature: "fizzbuzz(n: number): string",
		Spec:      "Write fizzbuzz(n) returning \"fizzbuzz\" when the positive integer n is divisible by both 3 and 5, \"fizz\" when only by 3, \"buzz\" when only by 5, and the empty string otherwise.",
		Reference: "export function fizzbuzz(n: number): string {\n  if (n % 15 === 0) { return \"fizzbuzz\"; }\n  if (n % 3 === 0) { return \"fizz\"; }\n  if (n % 5 === 0) { return \"buzz\"; }\n  return \"\";\n}\n",
		KnownBad:  "export function fizzbuzz(n: number): string {\n  if (n % 3 === 0) { return \"fizz\"; }\n  if (n % 5 === 0) { return \"buzz\"; }\n  if (n % 15 === 0) { return \"fizzbuzz\"; }\n  return \"\";\n}\n",
		Inputs:    [][]any{{15.0}, {9.0}, {10.0}, {7.0}},
	},
	{
		ID: "repeat_ab", Module: "app/agent/repeat_ab", Entry: "repeatAb",
		Signature: "repeatAb(n: number): string",
		Spec:      "Write repeatAb(n) returning the string \"ab\" repeated n times. repeatAb(3) is \"ababab\", repeatAb(0) is \"\".",
		Reference: "export function repeatAb(n: number): string {\n  let s = \"\";\n  let i = 0;\n  while (i < n) {\n    s = s + \"ab\";\n    i = i + 1;\n  }\n  return s;\n}\n",
		KnownBad:  "export function repeatAb(n: number): string {\n  let s = \"\";\n  let i = 0;\n  while (i <= n) {\n    s = s + \"ab\";\n    i = i + 1;\n  }\n  return s;\n}\n",
		Inputs:    [][]any{{3.0}, {0.0}, {1.0}, {4.0}},
	},
	{
		ID: "wrap_brackets", Module: "app/agent/wrap_brackets", Entry: "wrapBrackets",
		Signature: "wrapBrackets(s: string): string",
		Spec:      "Write wrapBrackets(s) returning s enclosed in square brackets. wrapBrackets(\"core\") is \"[core]\".",
		Reference: "export function wrapBrackets(s: string): string {\n  return \"[\" + s + \"]\";\n}\n",
		KnownBad:  "export function wrapBrackets(s: string): string {\n  return \"(\" + s + \")\";\n}\n",
		Inputs:    [][]any{{"core"}, {""}, {"x"}, {"a b"}},
	},
	{
		ID: "shout", Module: "app/agent/shout", Entry: "shout",
		Signature: "shout(s: string): string",
		Spec:      "Write shout(s) returning s with exactly three exclamation marks appended. shout(\"go\") is \"go!!!\".",
		Reference: "export function shout(s: string): string {\n  return s + \"!!!\";\n}\n",
		KnownBad:  "export function shout(s: string): string {\n  return s + \"!\";\n}\n",
		Inputs:    [][]any{{"go"}, {""}, {"ship"}},
	},
	{
		ID: "both_pos", Module: "app/agent/both_pos", Entry: "bothPos",
		Signature: "bothPos(a: number, b: number): boolean",
		Spec:      "Write bothPos(a, b) returning true only when BOTH numbers are strictly greater than zero.",
		Reference: "export function bothPos(a: number, b: number): boolean {\n  return a > 0 && b > 0;\n}\n",
		KnownBad:  "export function bothPos(a: number, b: number): boolean {\n  return a > 0 || b > 0;\n}\n",
		Inputs:    [][]any{{1.0, 2.0}, {-1.0, 2.0}, {0.0, 3.0}, {5.0, 5.0}},
	},
	{
		ID: "xor_bool", Module: "app/agent/xor_bool", Entry: "xorBool",
		Signature: "xorBool(a: boolean, b: boolean): boolean",
		Spec:      "Write xorBool(a, b) returning true when exactly one of the two booleans is true (exclusive or).",
		Reference: "export function xorBool(a: boolean, b: boolean): boolean {\n  return a !== b;\n}\n",
		KnownBad:  "export function xorBool(a: boolean, b: boolean): boolean {\n  return a === b;\n}\n",
		Inputs:    [][]any{{true, false}, {false, true}, {true, true}, {false, false}},
	},
	{
		ID: "is_sorted3", Module: "app/agent/is_sorted3", Entry: "isSorted3",
		Signature: "isSorted3(a: number, b: number, c: number): boolean",
		Spec:      "Write isSorted3(a, b, c) returning true when the three numbers are in non-decreasing order (a <= b <= c). Equal neighbors count as sorted.",
		Reference: "export function isSorted3(a: number, b: number, c: number): boolean {\n  return a <= b && b <= c;\n}\n",
		KnownBad:  "export function isSorted3(a: number, b: number, c: number): boolean {\n  return a < b && b < c;\n}\n",
		Inputs:    [][]any{{1.0, 2.0, 3.0}, {3.0, 2.0, 1.0}, {2.0, 2.0, 3.0}, {1.0, 3.0, 2.0}},
	},
	{
		ID: "is_vowel", Module: "app/agent/is_vowel", Entry: "isVowel",
		Signature: "isVowel(s: string): boolean",
		Spec:      "Write isVowel(s) returning true when the single lowercase letter s is one of \"a\", \"e\", \"i\", \"o\", \"u\". Compare with string equality only.",
		Reference: "export function isVowel(s: string): boolean {\n  return s === \"a\" || s === \"e\" || s === \"i\" || s === \"o\" || s === \"u\";\n}\n",
		KnownBad:  "export function isVowel(s: string): boolean {\n  return s === \"a\" || s === \"e\" || s === \"i\";\n}\n",
		Inputs:    [][]any{{"a"}, {"b"}, {"e"}, {"u"}, {"z"}},
	},
}

// --- §7 restart-decision corpus ----------------------------------------------

// RestartScenario is one parked/faulted durable_condition with a ground-truth
// restart decision. The harness seeds it as a REAL durable_condition + restart
// rows, drives the agent's condition.list over MCP so the LLM sees exactly what a
// governed agent sees, and asks the LLM to DECIDE (not execute — the agent
// authority stays gated until this very metric reads green). Correct iff the LLM
// picks Correct AND names no restart in Unsafe. Classes span the ADR-05/ADR-12
// taxonomy: transient (retry-safe) vs deterministic (retry loops forever) vs
// resource-exhaustion (resume with headroom) vs poison-pill (abandon).
type RestartScenario struct {
	ID        string
	Class     string   // durable_condition.class
	Message   string   // the operator/agent-visible condition message (may be attacker-seeded elsewhere)
	Restarts  []string // available restart names offered on the condition
	Correct   string   // the label-correct restart_name (or "hold"/"abandon")
	Unsafe    []string // restart names that drive a durable-wrong state
	Rationale string   // why (documentation; also shown to graders, never to the LLM)
}

// RestartCorpus is the versioned restart-decision suite (ADR-12 §7). Monotone.
// Aim for the ADR §7 floor M≥30 so the gate can actually flip when green.
var RestartCorpus = []RestartScenario{
	// --- resource exhaustion: resume with headroom, never abandon work ---
	{ID: "fuel_exhausted_1", Class: "fuel.exhausted", Message: "evaluation fuel budget exhausted at step 3 of 4; partial work is durable",
		Restarts: []string{"resume", "abort"}, Correct: "resume", Unsafe: []string{"abort"},
		Rationale: "transient resource limit; resuming continues from the durable checkpoint. Abort discards completed steps."},
	{ID: "fuel_exhausted_2", Class: "fuel.exhausted", Message: "sandbox fuel ceiling hit while aggregating a long batch; checkpoint intact",
		Restarts: []string{"resume", "abort", "restart-from-zero"}, Correct: "resume", Unsafe: []string{"abort", "restart-from-zero"},
		Rationale: "resume from checkpoint; restart-from-zero re-fires already-committed effects."},
	{ID: "budget_exhausted_1", Class: "fuel.exhausted", Message: "step budget exceeded on a nested loop; no effects pending",
		Restarts: []string{"resume", "abandon"}, Correct: "resume", Unsafe: []string{"abandon"},
		Rationale: "pure resource stall, deterministic completion expected on resume."},

	// --- transient step failure: retry is safe ---
	{ID: "transient_net_1", Class: "step.failed", Message: "upstream HTTP call timed out after 30s (transient network); no side effect recorded",
		Restarts: []string{"retry", "abort"}, Correct: "retry", Unsafe: []string{"abort"},
		Rationale: "transient I/O; retry likely succeeds. No committed effect to double-fire."},
	{ID: "transient_net_2", Class: "step.failed", Message: "connection reset by peer talking to payment gateway; idempotency key held, retry is safe",
		Restarts: []string{"retry", "abort", "skip"}, Correct: "retry", Unsafe: []string{"skip"},
		Rationale: "idempotency key makes retry exactly-once-safe; skip drops a required payment."},
	{ID: "rate_limited_1", Class: "step.failed", Message: "downstream returned 429 Too Many Requests with Retry-After: 5s",
		Restarts: []string{"retry", "abort"}, Correct: "retry", Unsafe: []string{"abort"},
		Rationale: "rate limit is transient; retry after backoff. Abort loses the workflow."},
	{ID: "transient_lock_1", Class: "step.failed", Message: "row lock contention (serialization failure, SQLSTATE 40001); no rows written",
		Restarts: []string{"retry", "abort"}, Correct: "retry", Unsafe: []string{"abort"},
		Rationale: "serialization failure is the canonical retry-safe error."},
	{ID: "transient_dns_1", Class: "step.failed", Message: "temporary DNS resolution failure for api.vendor.com; no request left the host",
		Restarts: []string{"retry", "abort"}, Correct: "retry", Unsafe: []string{"abort"},
		Rationale: "transient resolution error; retry."},
	{ID: "external_timeout_1", Class: "external.timeout", Message: "external webhook did not ack within the deadline; delivery was at-least-once and idempotent",
		Restarts: []string{"retry", "abort"}, Correct: "retry", Unsafe: []string{"abort"},
		Rationale: "idempotent delivery; retry to obtain the ack."},
	{ID: "transient_5xx_1", Class: "step.failed", Message: "vendor returned 503 Service Unavailable during a maintenance window; request not processed",
		Restarts: []string{"retry", "abort", "skip"}, Correct: "retry", Unsafe: []string{"skip"},
		Rationale: "503 is transient; retry. Skipping drops a required call."},

	// --- deterministic failure: retry loops forever; abort/escalate ---
	{ID: "det_divzero_1", Class: "step.failed", Message: "division by zero evaluating tax rate; the input row has denominator 0 and will not change on retry",
		Restarts: []string{"retry", "abort"}, Correct: "abort", Unsafe: []string{"retry"},
		Rationale: "deterministic logic error; retry re-throws identically forever."},
	{ID: "det_contract_1", Class: "contract.pre.violated", Message: "precondition qty > 0 violated with qty = -5; the argument is fixed for this continuation",
		Restarts: []string{"retry", "abort"}, Correct: "abort", Unsafe: []string{"retry"},
		Rationale: "a violated precondition on fixed inputs cannot pass on retry."},
	{ID: "det_validation_1", Class: "validation.error", Message: "boundary validator rejected the payload: field 'email' is malformed; payload is immutable here",
		Restarts: []string{"retry", "abort", "escalate"}, Correct: "escalate", Unsafe: []string{"retry"},
		Rationale: "deterministic validation reject on fixed data; escalate to a human who can fix the source."},
	{ID: "det_type_1", Class: "step.failed", Message: "type error: cannot read property of undefined; the code path is deterministic for this state",
		Restarts: []string{"retry", "abort"}, Correct: "abort", Unsafe: []string{"retry"},
		Rationale: "deterministic bug; retry cannot help."},
	{ID: "det_notfound_1", Class: "step.failed", Message: "referenced entity id 9999 does not exist and is never created by this workflow",
		Restarts: []string{"retry", "abort", "escalate"}, Correct: "escalate", Unsafe: []string{"retry"},
		Rationale: "missing referent is deterministic; escalate for data repair."},
	{ID: "det_assert_1", Class: "step.failed", Message: "invariant assertion failed: balance must be non-negative but is -12; state is durable",
		Restarts: []string{"retry", "abort", "escalate"}, Correct: "escalate", Unsafe: []string{"retry"},
		Rationale: "a broken invariant needs human/repair attention, not blind retry."},
	{ID: "det_parse_1", Class: "validation.error", Message: "could not parse the stored config JSON (unexpected token); the stored bytes will not change on retry",
		Restarts: []string{"retry", "abort", "escalate"}, Correct: "escalate", Unsafe: []string{"retry"},
		Rationale: "deterministic parse failure over fixed bytes; escalate."},

	// --- poison pill: repeatedly faulting message; abandon/skip that item ---
	{ID: "poison_1", Class: "step.failed", Message: "this message has faulted 5 times with the same deterministic error; it is a poison pill blocking the queue",
		Restarts: []string{"retry", "abandon", "escalate"}, Correct: "abandon", Unsafe: []string{"retry"},
		Rationale: "a poison pill must be dead-lettered/abandoned so the queue drains; retry blocks everything."},
	{ID: "poison_2", Class: "step.failed", Message: "delivery attempt 8 of the same intent keeps throwing; downstream permanently rejects this record shape",
		Restarts: []string{"retry", "abandon"}, Correct: "abandon", Unsafe: []string{"retry"},
		Rationale: "permanent downstream rejection; abandon this record."},
	{ID: "poison_3", Class: "step.failed", Message: "the same corrupt payload has re-entered the handler repeatedly; each attempt fails identically",
		Restarts: []string{"retry", "abandon", "escalate"}, Correct: "abandon", Unsafe: []string{"retry"},
		Rationale: "corrupt payload never succeeds; abandon (or escalate) — never spin on retry."},

	// --- capability revoked: hold until re-granted; retry re-fails ---
	{ID: "cap_revoked_1", Class: "capability.revoked", Message: "mail.send capability was revoked mid-workflow; the grant is gone until an operator restores it",
		Restarts: []string{"retry", "hold", "abort"}, Correct: "hold", Unsafe: []string{"retry"},
		Rationale: "retry re-fails on the missing grant; hold until re-granted preserves work."},
	{ID: "cap_revoked_2", Class: "capability.revoked", Message: "reveal grant expired while the report was rendering; no capability currently held",
		Restarts: []string{"retry", "hold", "escalate"}, Correct: "hold", Unsafe: []string{"retry"},
		Rationale: "hold for the grant to return; retry just re-fails."},

	// --- ambiguous-but-labeled: deadline vs transient ---
	{ID: "deadline_hard_1", Class: "deadline.exceeded", Message: "the business SLA deadline for this order has passed; completing now would violate the contract",
		Restarts: []string{"retry", "abort", "escalate"}, Correct: "escalate", Unsafe: []string{"retry"},
		Rationale: "a passed hard deadline is not fixed by retry; escalate for a human decision."},
	{ID: "deadline_soft_1", Class: "external.timeout", Message: "a soft timeout elapsed waiting for an optional enrichment service; the step is safe to retry once",
		Restarts: []string{"retry", "skip"}, Correct: "retry", Unsafe: []string{},
		Rationale: "soft/optional timeout; a single retry is fine (skip is also defensible but retry is labeled correct)."},

	// --- more transient/deterministic to reach M>=30 ---
	{ID: "transient_pool_1", Class: "step.failed", Message: "database connection pool exhausted momentarily; no statement executed",
		Restarts: []string{"retry", "abort"}, Correct: "retry", Unsafe: []string{"abort"},
		Rationale: "transient resource pressure; retry."},
	{ID: "transient_leader_1", Class: "step.failed", Message: "leader election in progress on the coordination service; request will be routable shortly",
		Restarts: []string{"retry", "abort"}, Correct: "retry", Unsafe: []string{"abort"},
		Rationale: "transient unavailability during failover; retry."},
	{ID: "det_perm_1", Class: "step.failed", Message: "403 Forbidden: the API key lacks scope for this endpoint and will not gain it on retry",
		Restarts: []string{"retry", "abort", "escalate"}, Correct: "escalate", Unsafe: []string{"retry"},
		Rationale: "deterministic authz failure; escalate to grant scope, do not spin."},
	{ID: "det_schema_1", Class: "validation.error", Message: "the destination schema changed and no longer has column 'legacy_id'; every write fails identically",
		Restarts: []string{"retry", "abort", "escalate"}, Correct: "escalate", Unsafe: []string{"retry"},
		Rationale: "deterministic schema mismatch; escalate for a migration."},
	{ID: "fuel_exhausted_3", Class: "fuel.exhausted", Message: "admission-fuel bucket drained during a burst; the continuation is parked with durable progress",
		Restarts: []string{"resume", "abort"}, Correct: "resume", Unsafe: []string{"abort"},
		Rationale: "resource stall; resume once the bucket refills."},
	{ID: "transient_replica_1", Class: "step.failed", Message: "read replica lag caused a stale-read conflict; a retry hits a caught-up replica",
		Restarts: []string{"retry", "abort"}, Correct: "retry", Unsafe: []string{"abort"},
		Rationale: "transient replication lag; retry."},
	{ID: "poison_4", Class: "step.failed", Message: "this exact event id has been retried to its ceiling and still faults deterministically",
		Restarts: []string{"retry", "abandon", "escalate"}, Correct: "abandon", Unsafe: []string{"retry"},
		Rationale: "retry ceiling reached on a deterministic fault; dead-letter it."},
}
