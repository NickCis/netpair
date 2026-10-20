package pair

import (
	"os"
	"reflect"
	"testing"
)

func TestParseNSpid(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   []int
	}{
		{
			name:   "nested pid namespace",
			status: "Name:\tsleep\nPid:\t1234\nNSpid:\t1234\t7\nPPid:\t1\n",
			want:   []int{1234, 7},
		},
		{
			name:   "deeper nesting",
			status: "NSpid:\t1234\t42\t1\n",
			want:   []int{1234, 42, 1},
		},
		{
			name:   "no NSpid line",
			status: "Name:\tsleep\nPid:\t1234\n",
			want:   nil,
		},
	}
	for _, c := range cases {
		if got := parseNSpid(c.status); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: parseNSpid = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProcPPid(t *testing.T) {
	ppid, ok := procPPid(os.Getpid())
	if !ok {
		t.Fatal("procPPid could not read own stat")
	}
	if want := os.Getppid(); ppid != want {
		t.Fatalf("procPPid = %d, want %d", ppid, want)
	}
}
