import { hostsResponse } from '$lib/server/hosts-refresh';
import type { RequestHandler } from './$types';

/** The org Hosts tab's background re-read: `GET /v1/orgs/{id}/hosts` as `{ hosts }`. */
export const GET: RequestHandler = (event) => hostsResponse(event, (id) => `/v1/orgs/${id}/hosts`);
