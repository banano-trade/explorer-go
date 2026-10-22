package ban

import (
	"strings"
	"testing"
)

func TestAddressRoundTrip(t *testing.T) {
	for _, a := range []string{
		"ban_1jung1eb3uomk1gsx7w6w7toqrikxm5pgn5wbsg5fpy96ckpdf6wmiuuzpca",
		"ban_1burnbabyburndiscoinferno111111111111111111111111111aj49sw3w",
		"ban_1ka1ium4pfue3uxtntqsrib8mumxgazsjf58gidh1xeo5te3whsq8z476goo",
	} {
		key, err := ParseAddress(a)
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		if got := Address(key); got != a {
			t.Fatalf("round trip: got %s want %s", got, a)
		}
	}
}

func TestAddressTypo(t *testing.T) {
	bad := "ban_1jung1eb3uomk1gsx7w6w7toqrikxm5pgn5wbsg5fpy96ckpdf6wmiuuzpcb"
	if _, err := ParseAddress(bad); err != ErrChecksum {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
	if _, err := ParseAddress("nano_1jung"); err != ErrNotAddress {
		t.Fatalf("want ErrNotAddress, got %v", err)
	}
}

func TestAmount(t *testing.T) {
	cases := map[string]string{
		"1382025178198578573031368846397047": "13,820.25",
		"518022490403961000000000000000":     "5.18",
		"100000000000000000000000000000":     "1",
		"0":                                  "0",
	}
	for raw, want := range cases {
		if got := FormatSmart(Raw(raw)); got != want {
			t.Errorf("%s: got %s want %s", raw, got, want)
		}
	}
	if got := FormatSmart(Raw("34000000000000000000000000000")); got != "0.34" {
		t.Errorf("small: got %s", got)
	}
	if got := Format(Raw("518022490403961000000000000000"), 29); !strings.HasPrefix(got, "5.18022490403961") {
		t.Errorf("full: got %s", got)
	}
}
