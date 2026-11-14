import type { Org, Project, User } from '$lib/api-types';

declare global {
	namespace App {
		// interface Error {}
		interface Locals {
			user: User | null;
			projects: Project[];
			/** The caller's organizations from `/v1/me` (api-v1 v1.24). */
			orgs: Org[];
			sessionUnavailable: boolean;
		}
		// interface PageData {}
		// interface PageState {}
		// interface Platform {}
	}
}

export {};
