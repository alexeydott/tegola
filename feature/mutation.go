package feature

import (
	"context"

	"github.com/alexeydott/tegola/provider"
)

// MutationCoordinator executes neutral mutations through policy,
// schema validation and a provider transaction. Adapters build
// provider.Mutation values; the coordinator owns the execution order:
// policy → structural validation → Begin → Apply → Commit.
type MutationCoordinator struct {
	// PolicyFor resolves the policy for a collection.
	PolicyFor func(collection string) Policy
	// SchemaFor resolves the schema descriptor for a collection.
	SchemaFor func(collection string) (*SchemaDescriptor, error)
	// ProviderFor resolves the MutationProvider and provider layer name
	// for a collection.
	ProviderFor func(collection string) (provider.MutationProvider, string, error)
}

// Execute runs one mutation as its own transaction.
func (c *MutationCoordinator) Execute(ctx context.Context, principal Principal, m provider.Mutation) (provider.MutationOutcome, provider.CommitReceipt, error) {
	var empty provider.MutationOutcome
	action, err := policyActionFor(m.Op.String())
	if err != nil {
		return empty, provider.CommitReceipt{}, &provider.MutationError{
			Kind:   provider.MutationErrMalformedInput,
			Reason: err.Error(),
		}
	}
	policy := c.PolicyFor(m.Collection)
	if d := policy.CheckCollection(ctx, principal, action, m.Collection); !d.Allow {
		return empty, provider.CommitReceipt{}, &provider.MutationError{
			Kind:   provider.MutationErrDenied,
			Reason: "collection policy denied: " + d.Reason,
		}
	}
	schema, err := c.SchemaFor(m.Collection)
	if err != nil {
		return empty, provider.CommitReceipt{}, err
	}
	if err := validateMutationInput(schema, m); err != nil {
		return empty, provider.CommitReceipt{}, err
	}
	mp, layer, err := c.ProviderFor(m.Collection)
	if err != nil {
		return empty, provider.CommitReceipt{}, err
	}
	if _, err := mp.DescribeWritable(ctx, layer); err != nil {
		return empty, provider.CommitReceipt{}, err
	}
	m.Collection = layer
	tx, err := mp.BeginFeatureTx(ctx, provider.TxOptions{})
	if err != nil {
		return empty, provider.CommitReceipt{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := ctx.Err(); err != nil {
		return empty, provider.CommitReceipt{}, &provider.MutationError{
			Kind:   provider.MutationErrMalformedInput,
			Reason: "context cancelled before begin",
		}
	}
	outcome, err := tx.Apply(ctx, m)
	if err != nil {
		return empty, provider.CommitReceipt{}, err
	}
	receipt, err := tx.Commit(ctx)
	if err != nil {
		return empty, provider.CommitReceipt{}, err
	}
	if receipt.Status == provider.CommitCommitted {
		committed = true
	}
	return outcome, receipt, nil
}

// validateMutationInput runs schema validation before Begin: unknown
// properties, read-only properties, type violations, required fields.
func validateMutationInput(schema *SchemaDescriptor, m provider.Mutation) error {
	if m.Op == provider.MutationDelete {
		if m.FeatureID == 0 {
			return &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "delete requires a feature ID"}
		}
		return nil
	}
	if m.Op == provider.MutationInsert && m.FeatureID != 0 {
		return &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: "insert must not carry a feature ID"}
	}
	if (m.Op == provider.MutationReplace || m.Op == provider.MutationUpdate) && m.FeatureID == 0 {
		return &provider.MutationError{Kind: provider.MutationErrMalformedInput, Reason: m.Op.String() + " requires a feature ID"}
	}
	present := make(map[string]TypedValue, len(m.Properties))
	for name, mv := range m.Properties {
		tv := mutationValueToTyped(schema, name, mv)
		present[name] = tv
		if err := schema.ValidateInputValue(name, tv); err != nil {
			return &provider.MutationError{Kind: provider.MutationErrSchemaViolation, Reason: err.Error()}
		}
	}
	if m.Op == provider.MutationInsert || m.Op == provider.MutationReplace {
		if err := schema.ValidateRequired(present); err != nil {
			return &provider.MutationError{Kind: provider.MutationErrSchemaViolation, Reason: err.Error()}
		}
	}
	return nil
}

func mutationValueToTyped(schema *SchemaDescriptor, name string, mv provider.MutationValue) TypedValue {
	tv := TypedValue{State: ValuePresent}
	if mv.Null {
		tv.State = ValueNull
	}
	if mv.Empty {
		tv.State = ValueEmpty
	}
	if desc, ok := schema.Property(name); ok {
		tv.Type = desc.Type
	}
	switch mv.Kind {
	case provider.MutationValueInteger:
		tv.Integer = mv.Integer
	case provider.MutationValueDecimal:
		tv.Decimal = mv.Decimal
	case provider.MutationValueString:
		tv.String = mv.String
	case provider.MutationValueBoolean:
		tv.Boolean = mv.Boolean
	}
	return tv
}
