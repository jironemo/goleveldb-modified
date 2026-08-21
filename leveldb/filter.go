// Copyright (c) 2012, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package leveldb

import (
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/learned"
)

type iFilter struct {
	filter.Filter
}

func (f iFilter) Contains(filter, key []byte) bool {
	ukey := internalKey(key).ukey()
	if learned.PredictExistence(ukey) >= learned.Tau {
		return true
	}
	return f.Filter.Contains(filter, ukey)
}

func (f iFilter) NewGenerator() filter.FilterGenerator {
	return iFilterGenerator{f.Filter.NewGenerator()}
}

type iFilterGenerator struct {
	filter.FilterGenerator
}

func (g iFilterGenerator) Add(key []byte) {
	ukey := internalKey(key).ukey()
	if learned.PredictExistence(ukey) < learned.Tau {
		g.FilterGenerator.Add(ukey)
	}
}
