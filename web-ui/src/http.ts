export class HTTPError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'HTTPError';
    this.status = status;
  }
}

export async function getJSON<T>(url: string): Promise<T> {
  const response = await fetch(url, {headers: {Accept: 'application/json'}});
  if (response.status === 401) {
    location.href = '/login';
    throw new HTTPError(401, 'unauthorized');
  }
  if (!response.ok) throw new HTTPError(response.status, `${response.status} ${(await response.text()).slice(0, 120)}`);
  return response.json() as Promise<T>;
}
