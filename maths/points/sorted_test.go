package points

import (
	"log"
	"reflect"
	"testing"

	"github.com/gdey/tbltest"
	"github.com/go-spatial/tegola/maths"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func TestSortAndUnique(t *testing.T) {
	type tcase struct {
		uspts []maths.Pt
		spts  []maths.Pt
	}
	fn := func(idx int, tc tcase) {
		gspts := SortAndUnique(tc.uspts)
		if !reflect.DeepEqual(tc.spts, gspts) {
			t.Errorf("[%v] did not sort and unique, Expected %v Got %v", idx, tc.spts, gspts)
		}
	}
	tbltest.Cases(
		tcase{},
		tcase{
			uspts: []maths.Pt{{X: 1, Y: 2}},
			spts:  []maths.Pt{{X: 1, Y: 2}},
		},
		tcase{
			uspts: []maths.Pt{{X: 1, Y: 2}, {X: 1, Y: 2}},
			spts:  []maths.Pt{{X: 1, Y: 2}},
		},
		tcase{
			uspts: []maths.Pt{{X: 1, Y: 2}, {X: 1, Y: 2}, {X: 3, Y: 4}, {X: 5, Y: 6}, {X: 5, Y: 6}},
			spts:  []maths.Pt{{X: 1, Y: 2}, {X: 3, Y: 4}, {X: 5, Y: 6}},
		},
		tcase{
			uspts: []maths.Pt{{X: 7, Y: 8}, {X: 1, Y: 2}, {X: 3, Y: 4}, {X: 5, Y: 6}, {X: 3, Y: 4}, {X: 1, Y: 2}, {X: 7, Y: 8}, {X: 2, Y: 3}, {X: 1, Y: 2}},
			spts:  []maths.Pt{{X: 1, Y: 2}, {X: 2, Y: 3}, {X: 3, Y: 4}, {X: 5, Y: 6}, {X: 7, Y: 8}},
		},
	).Run(fn)
}
