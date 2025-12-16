package stealth

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	"linkedin-automation-tool/config"
)

// MoveMouseHuman moves the mouse along a randomized cubic Bézier curve instead
// of a straight line. Straight lines with constant speed are a common bot
// signature; Bézier curves with variable speed, jitter, and occasional
// overshoot look closer to human hand movement.
func MoveMouseHuman(page *rod.Page, fromX, fromY, toX, toY int, cfg config.TimingConfig) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	dx := float64(toX - fromX)
	dy := float64(toY - fromY)
	dist := math.Hypot(dx, dy)

	if dist < 2 {
		return page.Mouse.MoveTo(proto.Point{X: float64(toX), Y: float64(toY)})
	}

	// Steps scale with distance; ensure minimum for curve smoothness.
	steps := int(dist/8) + 20
	if steps < 25 {
		steps = 25
	}
	if steps > 220 {
		steps = 220
	}

	// Random control points to vary curvature.
	cp1x := float64(fromX) + dx*0.3 + randRange(r, -dist*0.1, dist*0.1)
	cp1y := float64(fromY) + dy*0.3 + randRange(r, -dist*0.1, dist*0.1)
	cp2x := float64(fromX) + dx*0.7 + randRange(r, -dist*0.1, dist*0.1)
	cp2y := float64(fromY) + dy*0.7 + randRange(r, -dist*0.1, dist*0.1)

	// Optional overshoot then correct back to target.
	overshootChance := 0.22
	if r.Float64() < overshootChance {
		toX += int(randRange(r, -8, 14))
		toY += int(randRange(r, -8, 14))
	}

	points := make([]proto.Point, 0, steps+1)
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		x, y := cubicBezier(float64(fromX), cp1x, cp2x, float64(toX), t), cubicBezier(float64(fromY), cp1y, cp2y, float64(toY), t)

		// Micro jitter: subtle 1–3 px variation.
		x += randRange(r, -3, 3)
		y += randRange(r, -3, 3)

		points = append(points, proto.Point{X: x, Y: y})
	}

	// Replay points via MoveAlong so we control timing per step.
	idx := 0
	err := page.Mouse.MoveAlong(func() (proto.Point, bool) {
		if idx >= len(points) {
			return proto.Point{}, false
		}
		p := points[idx]
		idx++
		// Variable sleep to create acceleration/deceleration feel.
		sleep := RandomDelay(max(4, cfg.MinDelayMs/20), max(12, cfg.MinDelayMs/10))
		time.Sleep(sleep)
		return p, true
	})
	if err != nil {
		return fmt.Errorf("mouse move: %w", err)
	}

	// Correct to exact target.
	return page.Mouse.MoveTo(proto.Point{X: float64(toX), Y: float64(toY)})
}

// MoveToElementHuman moves the mouse to an element's center (with minor offset)
// using the human-like curve.
func MoveToElementHuman(page *rod.Page, el *rod.Element, cfg config.TimingConfig) error {
	// Increase timeout to 15 seconds for slower pages
	elTimed := el.Timeout(15 * time.Second)

	center, approxFrom, err := elementCenter(elTimed)
	if err != nil {
		return fmt.Errorf("get element center: %w", err)
	}
	cx, cy := center[0], center[1]

	// Start from current mouse position; Rod does not expose directly, so
	// approximate from element area; for realism, start slightly outside.
	startX := int(approxFrom[0])
	startY := int(approxFrom[1])

	return MoveMouseHuman(page, startX, startY, cx, cy, cfg)
}

func cubicBezier(p0, p1, p2, p3, t float64) float64 {
	u := 1 - t
	return u*u*u*p0 +
		3*u*u*t*p1 +
		3*u*t*t*p2 +
		t*t*t*p3
}

func randRange(r *rand.Rand, min, max float64) float64 {
	if max < min {
		min, max = max, min
	}
	return min + r.Float64()*(max-min)
}

func minf(vals []float64) float64 {
	min := vals[0]
	for _, v := range vals[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

func maxf(vals []float64) float64 {
	max := vals[0]
	for _, v := range vals[1:] {
		if v > max {
			max = v
		}
	}
	return max
}

// elementCenter finds the visual center of an element. Shape can be missing on
// some nodes; fall back to interactable point, then JS boundingClientRect.
func elementCenter(el *rod.Element) ([2]int, [2]float64, error) {
	// Try DOM shape first.
	if shape, err := el.Shape(); err == nil && len(shape.Quads) > 0 && len(shape.Quads[0]) >= 8 {
		quad := shape.Quads[0]
		xs := []float64{quad[0], quad[2], quad[4], quad[6]}
		ys := []float64{quad[1], quad[3], quad[5], quad[7]}
		cx := int((minf(xs) + maxf(xs)) / 2)
		cy := int((minf(ys) + maxf(ys)) / 2)
		return [2]int{cx, cy}, [2]float64{xs[0] - 12, ys[0] - 12}, nil
	}

	// Fallback 1: Rod's interactable point (already accounts for visibility/layout).
	if pt, err := el.WaitInteractable(); err == nil && pt != nil {
		return [2]int{int(pt.X), int(pt.Y)}, [2]float64{pt.X - 12, pt.Y - 12}, nil
	}

	// Fallback: use boundingClientRect via JS (increased timeout to 15s).
	res, err := el.Timeout(15 * time.Second).Evaluate(&rod.EvalOptions{
		ByValue: true,
		JS: `
(() => {
  const r = this.getBoundingClientRect();
  return {cx: r.x + r.width/2, cy: r.y + r.height/2, x: r.x, y: r.y};
})()`,
	})
	if err != nil {
		return [2]int{}, [2]float64{}, fmt.Errorf("element center js: %w", err)
	}

	val := res.Value
	cx := val.Get("cx")
	cy := val.Get("cy")
	x := val.Get("x")
	y := val.Get("y")
	if cx.Nil() || cy.Nil() || x.Nil() || y.Nil() {
		return [2]int{}, [2]float64{}, fmt.Errorf("element center js missing fields")
	}
	return [2]int{int(cx.Num()), int(cy.Num())}, [2]float64{x.Num() - 12, y.Num() - 12}, nil
}
