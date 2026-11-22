// Package fsbulk lists directories with getattrlistbulk(2) on Darwin, which
// returns names, types and sizes for a whole batch of entries per system
// call instead of one lstat each. It holds the primitives the storage walkers
// and detect's bundle walk share; record layouts specific to one caller stay
// with that caller. Off Darwin the package is empty and callers keep their
// portable ReadDir + lstat paths.
package fsbulk
