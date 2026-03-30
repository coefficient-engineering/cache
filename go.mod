module github.com/coefficient-engineering/cache

go 1.25.0

// v1.0.0 was published accidentally. v1.0.1 contains only this retraction.
retract (
	v1.0.1
	v1.0.0
)

require golang.org/x/sync v0.20.0
