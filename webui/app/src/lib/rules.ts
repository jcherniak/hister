// SPDX-License-Identifier: AGPL-3.0-or-later

import { apiFetch } from '#lib/api.js';

export type RuleType = 'allow' | 'skip' | 'priority' | 'versioning';
export type RuleMatchMode = 'domain' | 'url' | 'regex';

interface RuleLists {
  allow: string[];
  skip: string[];
  priority: string[];
  versioning: string[];
  visual_mode?: 'disabled' | 'opt-in' | 'opt-out';
  visual_sites?: Record<string, boolean>;
  visual_can_configure_install?: boolean;
}

export interface RulesData extends RuleLists {
  aliases: Record<string, string>;
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

export async function fetchRules(): Promise<RulesData> {
  const response = await apiFetch('/rules', { headers: { Accept: 'application/json' } });
  if (!response.ok) throw await responseError(response, 'Failed to load rules');
  const data = await response.json();
  return {
    allow: orDefault(data.allow, []),
    skip: orDefault(data.skip, []),
    priority: orDefault(data.priority, []),
    versioning: orDefault(data.versioning, []),
    aliases: orDefault(data.aliases, {}),
    visual_mode: data.visual_mode,
    visual_sites: orDefault(data.visual_sites, {}),
    visual_can_configure_install: data.visual_can_configure_install ?? false,
  };
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
