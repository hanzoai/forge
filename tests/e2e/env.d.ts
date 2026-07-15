declare namespace NodeJS {
  interface ProcessEnv {
    GIT_TEST_E2E_DOMAIN: string;
    GIT_TEST_E2E_USER: string;
    GIT_TEST_E2E_EMAIL: string;
    GIT_TEST_E2E_PASSWORD: string;
    GIT_TEST_E2E_URL: string;
  }
}
