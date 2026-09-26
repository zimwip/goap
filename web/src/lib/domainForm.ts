// Editing model of a shared domain (node types and link types). It reuses the
// row models of the methodology form.
import type { Domain } from './api';
import { algorithmFromForm, algorithmToForm, instanceFromForm, instanceToForm, type AlgorithmForm, type InstanceForm } from './algorithmForm';
import {
  lifecycleFromForm,
  lifecycleToForm,
  linkTypeFromForm,
  linkTypeToForm,
  nodeTypeFromForm,
  nodeTypeToForm,
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
  lifecycles: LifecycleForm[];
  algorithms: AlgorithmForm[];
  instances: InstanceForm[];
}

export function emptyDomainForm(): DomainForm {
  return { name: '', version: '0.1.0', description: '', nodeTypes: [], linkTypes: [], lifecycles: [], algorithms: [], instances: [] };
}

export function toDomainForm(d: Domain): DomainForm {
  return {
    name: d.name ?? '',
    version: d.version ?? '',
    description: d.description ?? '',
    nodeTypes: (d.nodeTypes ?? []).map(nodeTypeToForm),
    linkTypes: (d.linkTypes ?? []).map(linkTypeToForm),
    lifecycles: (d.lifecycles ?? []).map(lifecycleToForm),
    algorithms: (d.algorithms ?? []).map(algorithmToForm),
    instances: (d.algorithmInstances ?? []).map(instanceToForm),
  };
}

export function fromDomainForm(f: DomainForm): Domain {
  const d: Domain = { name: f.name.trim(), version: f.version.trim() };
  if (f.description.trim()) d.description = f.description.trim();
  if (f.nodeTypes.length) d.nodeTypes = f.nodeTypes.map(nodeTypeFromForm);
  if (f.linkTypes.length) d.linkTypes = f.linkTypes.map(linkTypeFromForm);
  if (f.lifecycles.length) d.lifecycles = f.lifecycles.map(lifecycleFromForm);
  if (f.algorithms.length) d.algorithms = f.algorithms.map(algorithmFromForm);
  if (f.instances.length) d.algorithmInstances = f.instances.map(instanceFromForm);
  return d;
}

/** Splits a domain reference "<name>[@<version>]". */
export function splitRef(ref: string): { name: string; version: string } {
  const [name, version = ''] = ref.trim().split('@');
  return { name, version };
}
