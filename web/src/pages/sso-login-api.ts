export const USER_LOGIN_MUTATION = `
  mutation SsoLogin($account: String!, $password: String!, $turnstileToken: String, $totpCode: String) {
    UserLogin(account: $account, password: $password, turnstile_token: $turnstileToken, totp_code: $totpCode) {
      token
    }
  }
`;

export const USER_REGISTER_MUTATION = `
  mutation SsoRegister($account: String!, $password: String!, $displayName: String!, $turnstileToken: String, $emailCode: String!) {
    UserRegister(account: $account, password: $password, display_name: $displayName, captcha: "", turnstile_token: $turnstileToken, email_code: $emailCode) {
      msg
    }
  }
`;

export const USER_REQUEST_EMAIL_CODE_MUTATION = `
  mutation RequestEmailCode($account: String!, $purpose: String!, $turnstileToken: String) {
    UserRequestEmailCode(account: $account, purpose: $purpose, turnstile_token: $turnstileToken) {
      msg
    }
  }
`;

export const USER_LOGIN_WITH_EMAIL_CODE_MUTATION = `
  mutation LoginWithEmailCode($account: String!, $emailCode: String!, $turnstileToken: String) {
    UserLoginWithEmailCode(account: $account, email_code: $emailCode, turnstile_token: $turnstileToken) {
      token
    }
  }
`;

export const USER_GITHUB_OAUTH_START_MUTATION = `
  mutation StartGithubOAuth($redirectTo: String, $turnstileToken: String) {
    UserGithubOAuthStart(redirect_to: $redirectTo, turnstile_token: $turnstileToken) {
      authorize_url
    }
  }
`;

export const USER_PASSKEY_LOGIN_START_MUTATION = `
  mutation StartPasskeyLogin($redirectTo: String, $turnstileToken: String) {
    UserStartPasskeyLogin(redirect_to: $redirectTo, turnstile_token: $turnstileToken) {
      options_json
      session
    }
  }
`;

export const USER_PASSKEY_LOGIN_FINISH_MUTATION = `
  mutation FinishPasskeyLogin($session: String!, $credentialJSON: String!) {
    UserFinishPasskeyLogin(session: $session, credential_json: $credentialJSON) {
      token
      redirect_to
    }
  }
`;

// SSO_SESSION_CHECK_QUERY is a lightweight authenticated probe used to confirm a
// stored session token still represents a live session before auto-redirecting an
// already-signed-in visitor. It selects the minimum field so the request only
// succeeds when the backend accepts the token (UserProfile validates the session).
export const SSO_SESSION_CHECK_QUERY = `
  query SsoSessionCheck {
    UserProfile {
      account
    }
  }
`;

// SsoLoginResponse describes the token returned by password login.
export interface SsoLoginResponse {
  UserLogin: {
    token: string;
  };
}

// SsoEmailCodeLoginResponse describes the token returned by email-code login.
export interface SsoEmailCodeLoginResponse {
  UserLoginWithEmailCode: {
    token: string;
  };
}

// SsoRegisterResponse describes the registration message returned by the backend.
export interface SsoRegisterResponse {
  UserRegister: {
    msg: string;
  };
}

// SsoEmailCodeResponse describes the response returned after requesting an email code.
export interface SsoEmailCodeResponse {
  UserRequestEmailCode: {
    msg: string;
  };
}

// SsoGithubOAuthStartResponse describes the authorization URL returned by the backend.
export interface SsoGithubOAuthStartResponse {
  UserGithubOAuthStart: {
    authorize_url: string;
  };
}

// SsoPasskeyStartResponse describes a passkey login challenge session.
export interface SsoPasskeyStartResponse {
  UserStartPasskeyLogin: {
    options_json: string;
    session: string;
  };
}

// SsoPasskeyLoginResponse describes a completed passkey login response.
export interface SsoPasskeyLoginResponse {
  UserFinishPasskeyLogin: {
    token: string;
    redirect_to: string;
  };
}
