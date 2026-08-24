package draft

import (
	"github.com/mandyville/mandyville-draft/squad"
)

// BestXI returns the highest-scoring legal starting XI for a single
// gameweek. It delegates to the shared squad package.
func BestXI(players map[int]*Player, gw int) ([]int, float64) {
	return squad.BestXI(players, gw)
}

// SwapValue computes the marginal starting-XI value of replacing `out` with
// `in` over the horizon. It delegates to the shared squad package.
func SwapValue(players map[int]*Player, out, in *Player, startGW, horizon int, discount float64) ([]float64, float64) {
	return squad.SwapValue(players, out, in, startGW, horizon, discount)
}
