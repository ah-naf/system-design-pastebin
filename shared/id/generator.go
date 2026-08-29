package id

type CounterSource interface {
	Next() (uint64, error)
}

type Generator struct {
	secret  uint64
	counter CounterSource
}

func NewGenerator(secret uint64, counter CounterSource) *Generator {
	return &Generator{
		secret:  secret,
		counter: counter,
	}
}

func (g *Generator) New() (string, error) {
	counterValue, err := g.counter.Next()
	if err != nil {
		return "", err
	}

	return Encode(counterValue ^ g.secret), nil
}
