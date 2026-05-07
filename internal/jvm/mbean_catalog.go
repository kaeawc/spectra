package jvm

import (
	"strings"

	"github.com/kaeawc/spectra/internal/appinspect"
)

const (
	MBeanOperationImpactInfo       = appinspect.OperationImpactInfo
	MBeanOperationImpactAction     = appinspect.OperationImpactAction
	MBeanOperationImpactActionInfo = appinspect.OperationImpactActionInfo
	MBeanOperationImpactUnknown    = appinspect.OperationImpactUnknown
)

type MBeanCatalog = appinspect.Catalog
type MBeanWatchSpec = appinspect.WatchSpec
type MBeanOperationSafety = appinspect.OperationSafety

func CatalogMBeans(result MBeansResult) MBeanCatalog {
	return appinspect.BuildCatalog(mbeanDescriptors(result.MBeans))
}

func MBeanDomainName(objectName string) string {
	if i := strings.IndexByte(objectName, ':'); i > 0 {
		return objectName[:i]
	}
	return objectName
}

func WatchableMBeanAttributes(result MBeansResult) []MBeanWatchSpec {
	return appinspect.WatchableAttributes(mbeanDescriptors(result.MBeans))
}

func OperationSafety(op MBeanOperation) MBeanOperationSafety {
	return appinspect.OperationSafetyFor(op.inspectOperation())
}

func (m MBean) InspectID() string {
	return m.Name
}

func (m MBean) InspectGroup() string {
	return MBeanDomainName(m.Name)
}

func (m MBean) InspectKind() string {
	return "mbean"
}

func (m MBean) InspectAttributes() []appinspect.Attribute {
	attrs := make([]appinspect.Attribute, 0, len(m.Attributes))
	for _, attr := range m.Attributes {
		attrs = append(attrs, appinspect.Attribute{
			Name:     attr.Name,
			Type:     attr.Type,
			Readable: attr.Readable,
			Writable: attr.Writable,
		})
	}
	return attrs
}

func (m MBean) InspectOperations() []appinspect.Operation {
	ops := make([]appinspect.Operation, 0, len(m.Operations))
	for _, op := range m.Operations {
		ops = append(ops, op.inspectOperation())
	}
	return ops
}

func (op MBeanOperation) inspectOperation() appinspect.Operation {
	params := make([]appinspect.Parameter, 0, len(op.Parameters))
	for _, param := range op.Parameters {
		params = append(params, appinspect.Parameter{Name: param.Name, Type: param.Type})
	}
	return appinspect.Operation{
		Name:       op.Name,
		ReturnType: op.ReturnType,
		Impact:     op.Impact,
		Parameters: params,
	}
}

func mbeanDescriptors(mbeans []MBean) []appinspect.ComponentDescriptor {
	components := make([]appinspect.ComponentDescriptor, 0, len(mbeans))
	for _, bean := range mbeans {
		components = append(components, bean)
	}
	return components
}
