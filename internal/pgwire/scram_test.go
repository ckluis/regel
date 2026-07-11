package pgwire

import "testing"

// TestSCRAMRFC7677Vector exercises the SCRAM-SHA-256 client against the RFC 7677
// §5 published test vector. The local pg_hba is trust auth, so the server never
// issues a SASL challenge; the client is validated against the RFC vector
// instead (and noted in the gate report).
func TestSCRAMRFC7677Vector(t *testing.T) {
	const (
		password    = "pencil"
		clientNonce = "rOprNGfwEbeRWgbNEkqO"
		serverFirst = "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
		wantFinal   = "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
		serverFinal = "v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="
	)
	c := newSCRAMClientWithNonce("user", password, clientNonce)
	first := c.clientFirst()
	if first != "n,,n=user,r="+clientNonce {
		t.Fatalf("client-first = %q", first)
	}
	final, err := c.clientFinal(serverFirst)
	if err != nil {
		t.Fatalf("clientFinal: %v", err)
	}
	if final != wantFinal {
		t.Fatalf("client-final mismatch:\n got  %q\n want %q", final, wantFinal)
	}
	if err := c.verifyServerFinal(serverFinal); err != nil {
		t.Fatalf("verifyServerFinal: %v", err)
	}
	// Negative: a tampered server signature must fail.
	c2 := newSCRAMClientWithNonce("user", password, clientNonce)
	c2.clientFirst()
	c2.clientFinal(serverFirst)
	if err := c2.verifyServerFinal("v=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); err == nil {
		t.Fatal("expected server signature mismatch")
	}
}
