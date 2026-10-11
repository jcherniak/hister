// SPDX-License-Identifier: AGPL-3.0-or-later

import { apiFetch } from '#lib/api.js';

export type RuleType = 'allow' | 'skip' | 'priority' | 'versioning';
export type RuleAction = RuleType | 'visual';
export type VisualRuleOption = 'opt-in' | 'opt-out';
export type RuleMatchMode = 'domain' | 'url' | 'regex';

interface RuleLists {
  allow: string[];
  skip: string[];
  priority: string[];
  versioning: string[];
  visual_mode?: 'disabled' | 'opt-in' | 'opt-out';
  visual_sites?: Record<string, boolean>;
  visual_rules?: Record<string, VisualRuleOption>;
  visual_can_configure_install?: boolean;
}

export interface RulesData extends RuleLists {
  aliases: Record<string, string>;
  visual: string[];
}

export interface RuleDraft {
  pattern?: string;
  domain?: string;
  exact_url?: string;
  include_subdomains?: boolean;
  url?: string;
}

export interface RulePreview {
  pattern: string;
  matches?: boolean;
}

export async function previewRule(draft: RuleDraft, signal?: AbortSignal): Promise<RulePreview> {
  const response = await apiFetch('/rules/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(draft),
    signal,
  });
  if (!response.ok) throw await responseError(response, 'Failed to check rule');
  return response.json();
}

async function responseError(response: Response, fallback: string): Promise<Error> {
  const body = await response.text();
  return new Error(body.trim() || fallback);
}

function orDefault<T>(value: T | null | undefined, fallback: T): T {
  return value ?? fallback;
}

export function visualOnlyPatterns(rules: RuleLists): string[] {
  const indexed = new Set([...rules.allow, ...rules.skip, ...rules.priority, ...rules.versioning]);
  return Object.keys(rules.visual_rules ?? {}).filter((pattern) => !indexed.has(pattern));
}

export async function fetchRules(): Promise<RulesData> {
  const response = await apiFetch('/rules', { headers: { Accept: 'application/json' } });
  if (!response.ok) throw await responseError(response, 'Failed to load rules');
  const data = await response.json();
  const rules: RulesData = {
    allow: orDefault(data.allow, []),
    skip: orDefault(data.skip, []),
    priority: orDefault(data.priority, []),
    versioning: orDefault(data.versioning, []),
    aliases: orDefault(data.aliases, {}),
    visual: [],
    visual_mode: data.visual_mode,
    visual_sites: orDefault(data.visual_sites, {}),
    visual_rules: orDefault(data.visual_rules, {}),
    visual_can_configure_install: data.visual_can_configure_install ?? false,
  };
  rules.visual = visualOnlyPatterns(rules);
  return rules;
}

export async function saveRuleLists(rules: RuleLists): Promise<void> {
  const formData = new URLSearchParams();
  formData.set('allow', rules.allow.join('\n'));
  formData.set('skip', rules.skip.join('\n'));
  formData.set('priority', rules.priority.join('\n'));
  formData.set('versioning', rules.versioning.join('\n'));
  if (rules.visual_sites !== undefined) {
    formData.set('visual_sites', JSON.stringify(rules.visual_sites));
  }
  if (rules.visual_rules !== undefined) {
    formData.set('visual_rules', JSON.stringify(rules.visual_rules));
  }
  const response = await apiFetch('/rules', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: formData.toString(),
  });
  if (!response.ok) throw await responseError(response, 'Failed to save rules');
}

export async function saveVisualSettings(settings: {
  visual_mode?: RuleLists['visual_mode'];
  visual_sites?: Record<string, boolean>;
}): Promise<void> {
  const formData = new URLSearchParams();
  if (settings.visual_mode !== undefined) formData.set('visual_mode', settings.visual_mode);
  if (settings.visual_sites !== undefined) {
    formData.set('visual_sites', JSON.stringify(settings.visual_sites));
  }
  const response = await apiFetch('/rules', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: formData.toString(),
  });
  if (!response.ok) throw await responseError(response, 'Failed to save visual archive settings');
}
