// Editing model of a domain (node types and link types). It reuses the
// row models of the methodology form.
import type { ChangeObjectType, Domain } from './api';
import { algorithmFromForm, algorithmToForm, instanceFromForm, instanceToForm, type AlgorithmForm, type InstanceForm } from './algorithmForm';
import {
  enumFromForm,
  enumToForm,
  lifecycleFromForm,
  lifecycleToForm,
  linkTypeFromForm,
  linkTypeToForm,
  nodeTypeFromForm,
  nodeTypeToForm,
  type EnumForm,
  type LifecycleForm,
  type LinkTypeForm,
  type NodeTypeForm,
} from './methodologyForm';

export interface DomainForm {
  name: string;
  version: string;
  description: string;
  nodeTypes: NodeTypeForm[];
  linkTypes: LinkTypeForm[];
  enums: EnumForm[];
  lifecycles: LifecycleForm[];
  algorithms: AlgorithmForm[];
  instances: InstanceForm[];
  /** change object types (ADR 0098), kept as they are: no editor of them yet, a save must not drop them */
  changeObjectTypes: ChangeObjectType[];
}

export function emptyDomainForm(): DomainForm {
  return { name: '', version: '0.1.0', description: '', nodeTypes: [], linkTypes: [], enums: [], lifecycles: [], algorithms: [], instances: [], changeObjectTypes: [] };
}

export function toDomainForm(d: Domain): DomainForm {
  return {
    name: d.name ?? '',
    version: d.version ?? '',
    description: d.description ?? '',
    nodeTypes: (d.nodeTypes ?? []).map(nodeTypeToForm),
    linkTypes: (d.linkTypes ?? []).map(linkTypeToForm),
    enums: (d.enums ?? []).map(enumToForm),
    lifecycles: (d.lifecycles ?? []).map(lifecycleToForm),
    algorithms: (d.algorithms ?? []).map(algorithmToForm),
    instances: (d.algorithmInstances ?? []).map(instanceToForm),
    changeObjectTypes: structuredClone(d.changeObjectTypes ?? []),
  };
}

export function fromDomainForm(f: DomainForm): Domain {
  const d: Domain = { name: f.name.trim(), version: f.version.trim() };
  if (f.description.trim()) d.description = f.description.trim();
  if (f.nodeTypes.length) d.nodeTypes = f.nodeTypes.map(nodeTypeFromForm);
  if (f.linkTypes.length) d.linkTypes = f.linkTypes.map(linkTypeFromForm);
  if (f.enums.length) d.enums = f.enums.map(enumFromForm);
  if (f.lifecycles.length) d.lifecycles = f.lifecycles.map(lifecycleFromForm);
  if (f.algorithms.length) d.algorithms = f.algorithms.map(algorithmFromForm);
  if (f.instances.length) d.algorithmInstances = f.instances.map(instanceFromForm);
  if (f.changeObjectTypes.length) d.changeObjectTypes = structuredClone(f.changeObjectTypes);
  return d;
}

