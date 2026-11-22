import { hostsResponse } from '$lib/server/hosts-refresh';
import type { RequestHandler } from './$types';

/** The project Hosts tab's background re-read: `GET /v1/projects/{id}/hosts` as `{ hosts }`. */
export const GET: RequestHandler = (event) => hostsResponse(event, (id) => `/v1/projects/${id}/hosts`);
