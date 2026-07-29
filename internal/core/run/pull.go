package run

type LayerPhase string

const (
	LayerWaiting     LayerPhase = "waiting"
	LayerDownloading LayerPhase = "downloading"
	LayerVerifying   LayerPhase = "verifying"
	LayerExtracting  LayerPhase = "extracting"
	LayerComplete    LayerPhase = "complete"
	LayerExists      LayerPhase = "exists"
)

func (p LayerPhase) Done() bool { return p == LayerComplete || p == LayerExists }

const downloadShare = 0.5

type Layer struct {
	ID      string
	Phase   LayerPhase
	Current int64
	Total   int64
	Size    int64
}

func (l Layer) Fraction() float64 {
	switch l.Phase {
	case LayerComplete, LayerExists:
		return 1
	case LayerDownloading:
		return downloadShare * ratio(l.Current, l.Total)
	case LayerVerifying:
		return downloadShare
	case LayerExtracting:
		return downloadShare * (1 + ratio(l.Current, l.Total))
	default:
		return 0
	}
}

func ratio(current, total int64) float64 {
	switch {
	case total <= 0, current <= 0:
		return 0
	case current >= total:
		return 1
	default:
		return float64(current) / float64(total)
	}
}

type PullStatus struct {
	Image  string
	Layers []Layer
}

func (s PullStatus) Complete() int {
	n := 0
	for _, l := range s.Layers {
		if l.Phase.Done() {
			n++
		}
	}
	return n
}

func (s PullStatus) Bytes() (current, total int64) {
	for _, l := range s.Layers {
		if l.Size <= 0 {
			continue
		}
		total += l.Size
		if l.Phase == LayerDownloading {
			current += min(l.Current, l.Size)
			continue
		}
		if l.Phase != LayerWaiting {
			current += l.Size
		}
	}
	return current, total
}

func (s PullStatus) Percent() int {
	if len(s.Layers) == 0 {
		return 0
	}
	sum := 0.0
	for _, l := range s.Layers {
		sum += l.Fraction()
	}
	return int(sum / float64(len(s.Layers)) * 100)
}
