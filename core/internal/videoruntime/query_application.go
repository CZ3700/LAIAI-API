package videoruntime

// QueryApplication fixes the query reducer policy independently of the shared
// wire schema. A zero value never authorizes either application policy.
type QueryApplication string

const (
	QueryApplicationAtomicV1               QueryApplication = "atomic_v1"
	QueryApplicationGenerationSettlementV2 QueryApplication = "generation_then_settlement_v2"
)

// ResolveQueryApplication accepts only a complete compiled contract and its
// exact observation schema. It does not select a producer's default runtime.
func ResolveQueryApplication(contract Contract, observationSchema uint32) (QueryApplication, error) {
	if observationSchema != ObservationSchemaV1 {
		return "", ErrUnsupported
	}
	switch contract {
	case LegacyV1():
		return QueryApplicationAtomicV1, nil
	case GenerationSettlementV2():
		return QueryApplicationGenerationSettlementV2, nil
	default:
		return "", ErrUnsupported
	}
}
