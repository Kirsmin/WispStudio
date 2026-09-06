package toolcall

import "testing"

func TestFramerEverySplit(t *testing.T) {
	input := "先看时间。<tc:Time />"
	for split := 0; split <= len(input); split++ {
		var f Framer
		a := f.Feed(input[:split])
		b := f.Feed(input[split:])
		if a.Err != nil || b.Err != nil {
			t.Fatalf("split %d: %v %v", split, a.Err, b.Err)
		}
		if a.Text+b.Text != "先看时间。" {
			t.Fatalf("split %d text=%q", split, a.Text+b.Text)
		}
		call := a.Call
		if call == nil {
			call = b.Call
		}
		if call == nil || call.Name != "Time" || call.Raw != "<tc:Time />" {
			t.Fatalf("split %d call=%#v", split, call)
		}
	}
}

func TestFramerOrdinaryLessThan(t *testing.T) {
	var f Framer
	var got string
	for _, chunk := range []string{"a ", "<", " b ", "<t", "est>"} {
		r := f.Feed(chunk)
		if r.Err != nil || r.Call != nil {
			t.Fatalf("unexpected result: %#v", r)
		}
		got += r.Text
	}
	got += f.Finalize().Text
	if got != "a < b <test>" {
		t.Fatalf("got %q", got)
	}
}

func TestPairedTool(t *testing.T) {
	var f Framer
	r := f.Feed("<tc:Time></tc:Time>")
	if r.Err != nil || r.Call == nil || r.Call.Name != "Time" {
		t.Fatalf("unexpected result: %#v", r)
	}
}

func TestFramerRejectsSpaceAfterPrefix(t *testing.T) {
	f := &Framer{}
	r := f.Feed("<tc: Time />")
	if r.Err == nil {
		t.Fatal("expected malformed tool call error")
	}
}
