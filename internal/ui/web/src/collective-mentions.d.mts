import type { Candidate } from './features/Composer.mentions';
import type { T } from './api';
export function collectiveOptions(people: Candidate[], view: T.TeamsView | null): Candidate[];
