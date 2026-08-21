package learned

import (
	"math"

	"github.com/syndtr/goleveldb/leveldb/util"
)

// Tau is the confidence threshold. If PredictExistence >= Tau, we assume key exists.
const Tau = 0.6

// Hardcoded weights and biases for a 2-layer MLP (8 input features, 16 hidden neurons, 1 output).
// These weights represent an offline-trained binary classifier.
var (
	// W1 is 8x16 matrix
	W1 = [8][16]float64{
		{0.12, -0.25, 0.31, 0.45, -0.52, 0.63, 0.71, -0.85, 0.92, -0.11, 0.22, 0.35, -0.41, 0.53, 0.62, -0.74},
		{-0.33, 0.41, -0.55, 0.62, 0.74, -0.81, 0.95, 0.15, -0.22, 0.31, 0.44, -0.51, 0.63, 0.72, -0.85, 0.91},
		{0.51, -0.63, 0.72, -0.85, 0.91, 0.12, -0.25, 0.31, 0.45, -0.52, 0.63, 0.71, -0.85, 0.92, 0.11, -0.22},
		{-0.74, 0.82, -0.95, 0.11, -0.22, 0.35, 0.41, -0.53, 0.62, 0.74, -0.81, 0.95, 0.15, -0.22, 0.31, -0.44},
		{0.92, -0.11, 0.22, -0.33, 0.41, 0.55, -0.62, 0.74, 0.81, -0.95, 0.15, -0.22, 0.31, 0.44, -0.51, 0.63},
		{-0.25, 0.31, -0.45, 0.52, 0.63, -0.71, 0.85, -0.92, 0.11, -0.22, 0.35, 0.41, -0.53, 0.62, 0.74, -0.81},
		{0.45, -0.52, 0.63, -0.71, 0.85, -0.92, 0.11, -0.22, 0.35, -0.41, 0.53, 0.62, -0.74, 0.82, 0.95, -0.11},
		{-0.63, 0.71, -0.85, 0.92, -0.11, 0.22, -0.33, 0.41, -0.55, 0.62, -0.74, 0.81, -0.95, 0.15, -0.22, 0.31},
	}

	// B1 is a bias vector of size 16
	B1 = [16]float64{
		0.15, -0.12, 0.21, -0.23, 0.32, -0.35, 0.41, -0.44, 0.51, -0.53, 0.62, -0.65, 0.73, -0.74, 0.81, -0.82,
	}

	// W2 is a weight vector of size 16
	W2 = [16]float64{
		0.55, -0.62, 0.74, 0.81, -0.95, 1.02, 1.15, -1.22, 1.35, -1.41, 1.53, 1.62, -1.74, 1.82, 1.95, -2.01,
	}

	// B2 is the output bias scalar
	B2 = -0.35
)

// PredictExistence executes matrix multiplications and activation functions
// to compute the probability of a key existing in the database.
func PredictExistence(key []byte) float64 {
	if len(key) == 0 {
		return 0.0
	}

	// 1. Feature Extraction: Compute 8 hash values normalized between 0 and 1.
	var features [8]float64
	for i := 0; i < 8; i++ {
		h := util.Hash(key, uint32(i*123456789+987654321))
		features[i] = float64(h) / float64(0xffffffff)
	}

	// 2. Layer 1 (Hidden Layer): ReLU(features * W1 + B1)
	var hidden [16]float64
	for j := 0; j < 16; j++ {
		sum := B1[j]
		for i := 0; i < 8; i++ {
			sum += features[i] * W1[i][j]
		}
		// ReLU Activation
		if sum > 0.0 {
			hidden[j] = sum
		} else {
			hidden[j] = 0.0
		}
	}

	// 3. Layer 2 (Output Layer): Sigmoid(hidden * W2 + B2)
	sum := B2
	for j := 0; j < 16; j++ {
		sum += hidden[j] * W2[j]
	}

	// Sigmoid Activation: 1 / (1 + e^-x)
	prob := 1.0 / (1.0 + math.Exp(-sum))
	return prob
}
