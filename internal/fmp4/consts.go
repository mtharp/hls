package fmp4

// fragment sample flags
const (
	FragSampleIsNonSync       = 0x00010000
	FragSampleHasDependencies = 0x01000000
	FragSampleNoDependencies  = 0x02000000
)
