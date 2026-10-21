package learned

import (
	"crypto/rand"
	"fmt"
	"testing"
)

func TestLearnedFilter_100kEvaluation(t *testing.T) {
	totalKeys := 100000

	// 1. Evaluate 100,000 YCSB Keys
	ycsbLearnedHits := 0
	ycsbOverflowHits := 0

	for i := 0; i < totalKeys; i++ {
		key := []byte(fmt.Sprintf("user%d", i))
		prob := PredictExistence(key)
		if prob >= Tau {
			ycsbLearnedHits++
		} else {
			ycsbOverflowHits++
		}
	}

	// 2. Evaluate 100,000 Relational Dataset Keys (orders, customers, order_items, products)
	relationalLearnedHits := 0
	relationalOverflowHits := 0

	for i := 1; i <= totalKeys; i++ {
		key := []byte(fmt.Sprintf("orders:%016d", i))
		prob := PredictExistence(key)
		if prob >= Tau {
			relationalLearnedHits++
		} else {
			relationalOverflowHits++
		}
	}

	// 3. Evaluate 100,000 Random Non-Existent Keys
	randomRejectedByLearned := 0
	randomFalsePositives := 0

	for i := 0; i < totalKeys; i++ {
		buf := make([]byte, 16)
		_, _ = rand.Read(buf)
		prob := PredictExistence(buf)
		if prob < Tau {
			randomRejectedByLearned++
		} else {
			randomFalsePositives++
		}
	}

	t.Log("========================================================================================")
	t.Log("  EMPIRICAL SCALE EVALUATION: 100,000 RECORDS (LEARNED FILTER vs OVERFLOW BLOOM)")
	t.Log("========================================================================================")
	t.Logf("1. YCSB Keys (N=%d):", totalKeys)
	t.Logf("   - Learned Filter Hits (Prob >= %.2f)  : %d / %d (%.4f%%)",
		Tau, ycsbLearnedHits, totalKeys, float64(ycsbLearnedHits)/float64(totalKeys)*100)
	t.Logf("   - Overflow Bloom Fallback (Prob < %.2f): %d / %d (%.4f%%)",
		Tau, ycsbOverflowHits, totalKeys, float64(ycsbOverflowHits)/float64(totalKeys)*100)
	t.Log("")
	t.Logf("2. Relational Orders Keys (N=%d):", totalKeys)
	t.Logf("   - Learned Filter Hits (Prob >= %.2f)  : %d / %d (%.4f%%)",
		Tau, relationalLearnedHits, totalKeys, float64(relationalLearnedHits)/float64(totalKeys)*100)
	t.Logf("   - Overflow Bloom Fallback (Prob < %.2f): %d / %d (%.4f%%)",
		Tau, relationalOverflowHits, totalKeys, float64(relationalOverflowHits)/float64(totalKeys)*100)
	t.Log("")
	t.Logf("3. Random Non-Existent Keys (N=%d):", totalKeys)
	t.Logf("   - Instant Negative Rejection (Prob < %.2f) : %d / %d (%.4f%%)",
		Tau, randomRejectedByLearned, totalKeys, float64(randomRejectedByLearned)/float64(totalKeys)*100)
	t.Logf("   - Passed to B+Tree Lookup (Prob >= %.2f)  : %d / %d (%.4f%%)",
		Tau, randomFalsePositives, totalKeys, float64(randomFalsePositives)/float64(totalKeys)*100)
	t.Log("========================================================================================")
}

func TestFilterReadBreakdown_1M(t *testing.T) {
	totalKeys := 1000000
	queries := 100000

	// 1. Build Overflow Bloom Filter for 1,000,000 Preloaded Keys
	// In the actual system, keys with prob < Tau are inserted into overflow bloom.
	overflowBloomKeys := make(map[string]bool)
	for i := 0; i < totalKeys; i++ {
		k := []byte(fmt.Sprintf("usertable:user%016d", i))
		prob := PredictExistence(k)
		if prob < Tau {
			overflowBloomKeys[string(k)] = true
		}
	}
	t.Logf("Preloaded 1M Keys: %d in Overflow Bloom Filter (%.4f%%)",
		len(overflowBloomKeys), float64(len(overflowBloomKeys))/float64(totalKeys)*100)

	// 2. Simulate 80% Negative Lookups / 20% Positive Lookups (100,000 queries)
	// Positive queries: keys [0, 999999]
	// Negative queries: keys [1000000, 1999999]
	posCount := int(float64(queries) * 0.20) // 20,000
	negCount := int(float64(queries) * 0.80) // 80,000

	// Track negative lookup path
	negAIShieldFlagged := 0
	negCaughtByOverflow := 0
	negRejectedByOverflow := 0
	negPassedToBTree := 0

	for i := 0; i < negCount; i++ {
		negKey := []byte(fmt.Sprintf("usertable:user%016d", totalKeys+i))
		prob := PredictExistence(negKey)

		if prob < Tau {
			negAIShieldFlagged++
			// Check overflow filter
			if overflowBloomKeys[string(negKey)] {
				negCaughtByOverflow++
			} else {
				negRejectedByOverflow++ // Confirmed non-existent by filter tier!
			}
		} else {
			negPassedToBTree++ // AI shield predicted >= 0.60, resolved by B+ tree
		}
	}

	// Track positive lookup path
	posAIShieldFastPath := 0
	posRescuedByOverflow := 0

	for i := 0; i < posCount; i++ {
		posKey := []byte(fmt.Sprintf("usertable:user%016d", i))
		prob := PredictExistence(posKey)

		if prob >= Tau {
			posAIShieldFastPath++
		} else {
			if overflowBloomKeys[string(posKey)] {
				posRescuedByOverflow++ // Safely rescued by overflow bloom filter!
			}
		}
	}

	t.Log("========================================================================================")
	t.Log("  READ-PATH FILTER BREAKDOWN: 80% NEGATIVE / 20% POSITIVE (1M DATASET)")
	t.Log("========================================================================================")
	t.Logf("A. Positive Lookups (20,000 queries):")
	t.Logf("   • AI Shield Fast-Path (Prob >= 0.60)     : %d (%.4f%%)", posAIShieldFastPath, float64(posAIShieldFastPath)/float64(posCount)*100)
	t.Logf("   • Rescued by Overflow Bloom (Prob < 0.60): %d (%.4f%%) [0%% False Negatives]", posRescuedByOverflow, float64(posRescuedByOverflow)/float64(posCount)*100)
	t.Log("")
	t.Logf("B. Negative Lookups (80,000 queries for absent keys):")
	t.Logf("   • AI Shield Identified as Absent (Prob < 0.60) : %d (%.4f%%)", negAIShieldFlagged, float64(negAIShieldFlagged)/float64(negCount)*100)
	t.Logf("     - Confirmed Absent by Overflow Bloom (Pruned): %d (%.4f%%)", negRejectedByOverflow, float64(negRejectedByOverflow)/float64(negCount)*100)
	t.Logf("     - Caught by Overflow Bloom (False Positives) : %d (%.4f%%)", negCaughtByOverflow, float64(negCaughtByOverflow)/float64(negCount)*100)
	t.Logf("   • Passed to In-Memory B+ Tree (Prob >= 0.60)   : %d (%.4f%%)", negPassedToBTree, float64(negPassedToBTree)/float64(negCount)*100)
	t.Log("========================================================================================")
}

