/**
 * Generated types for the backend's OpenAPI document. DO NOT EDIT BY HAND.
 *
 * Regenerate with `just gen-api` (or `pnpm gen:api`) whenever the backend's API changes. That is
 * the whole point of this file: a removed field or a renamed path becomes a compile error here
 * instead of `undefined` in a browser.
 *
 * This copy is a SEED, hand-written to match the endpoints the generated project ships with, so
 * that `just check` passes on a clone that has never had the backend running. The first
 * `just gen-api` replaces it wholesale, and from then on nothing in this file is authored.
 */

export interface paths {
  "/health/live": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Liveness: the process is up. Touches no dependency, by design. */
    get: {
      parameters: {
        query?: never;
        header?: never;
        path?: never;
        cookie?: never;
      };
      requestBody?: never;
      responses: {
        200: {
          headers: Record<string, unknown>;
          content: {
            "application/json": Record<string, string>;
          };
        };
      };
    };
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/health/ready": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /** Readiness: the process can serve correct answers. 503 when a dependency is down. */
    get: {
      parameters: {
        query?: never;
        header?: never;
        path?: never;
        cookie?: never;
      };
      requestBody?: never;
      responses: {
        200: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["ReadinessStatus"];
          };
        };
      };
    };
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
{%- if cookiecutter.use_postgres == "yes" %}
  "/api/v1/items": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get: {
      parameters: {
        query?: {
          limit?: number;
          offset?: number;
        };
        header?: never;
        path?: never;
        cookie?: never;
      };
      requestBody?: never;
      responses: {
        200: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["ItemList"];
          };
        };
        422: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["HTTPValidationError"];
          };
        };
      };
    };
    put?: never;
    post: {
      parameters: {
        query?: never;
        header?: never;
        path?: never;
        cookie?: never;
      };
      requestBody: {
        content: {
          "application/json": components["schemas"]["ItemCreate"];
        };
      };
      responses: {
        201: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["ItemRead"];
          };
        };
        409: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["ErrorResponse"];
          };
        };
        422: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["ErrorResponse"];
          };
        };
      };
    };
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/api/v1/items/{item_id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get: {
      parameters: {
        query?: never;
        header?: never;
        path: {
          item_id: string;
        };
        cookie?: never;
      };
      requestBody?: never;
      responses: {
        200: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["ItemRead"];
          };
        };
        404: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["ErrorResponse"];
          };
        };
      };
    };
    put?: never;
    post?: never;
    delete: {
      parameters: {
        query?: never;
        header?: never;
        path: {
          item_id: string;
        };
        cookie?: never;
      };
      requestBody?: never;
      responses: {
        /** No content. */
        204: {
          headers: Record<string, unknown>;
          content?: never;
        };
        404: {
          headers: Record<string, unknown>;
          content: {
            "application/json": components["schemas"]["ErrorResponse"];
          };
        };
      };
    };
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
{%- endif %}
}

export type webhooks = Record<string, never>;

export interface components {
  schemas: {
    ReadinessStatus: {
      status: string;
      version: string;
{%- if cookiecutter.use_postgres == "yes" %}
      database: string;
{%- endif %}
    };
    ErrorDetail: {
      code: string;
      message: string;
      trace_id?: string | null;
    };
    ErrorResponse: {
      error: components["schemas"]["ErrorDetail"];
    };
{%- if cookiecutter.use_postgres == "yes" %}
    ItemCreate: {
      name: string;
      description?: string | null;
    };
    ItemRead: {
      /** Format: uuid */
      id: string;
      name: string;
      description: string | null;
      /** Format: date-time */
      created_at: string;
      /** Format: date-time */
      updated_at: string;
    };
    ItemList: {
      items: components["schemas"]["ItemRead"][];
      count: number;
    };
    ValidationError: {
      loc: (string | number)[];
      msg: string;
      type: string;
    };
    HTTPValidationError: {
      detail?: components["schemas"]["ValidationError"][];
    };
{%- endif %}
  };
  responses: never;
  parameters: never;
  requestBodies: never;
  headers: never;
  pathItems: never;
}

export type $defs = Record<string, never>;

export type operations = Record<string, never>;
