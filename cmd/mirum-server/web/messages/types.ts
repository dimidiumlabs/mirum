// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";

export interface Messages {
  app_name: string;
  company_name: string;
  sign_out: string;
  back_to_home: string;
  loading: string;

  dashboard_organizations: string;
  dashboard_no_organizations: string;

  login_title: string;
  login_email_label: string;
  login_email_placeholder: string;
  login_password_label: string;
  login_submit: string;
  login_contact_admin: string;
  login_terms: (terms: ReactNode, privacy: ReactNode) => ReactNode;
  login_terms_of_service: string;
  login_privacy_policy: string;

  error_400_title: string;
  error_400_body: string;
  error_401_title: string;
  error_401_body: string;
  error_403_title: string;
  error_403_body: string;
  error_404_title: string;
  error_404_body: string;
  error_405_title: string;
  error_405_body: string;
  error_500_title: string;
  error_500_body: string;
  error_503_title: string;
  error_503_body: string;
  error_unknown_body: string;

  err_user_not_found: string;
  err_org_not_found: string;
  err_worker_not_found: string;
  err_member_not_found: string;
  err_email_taken: string;
  err_slug_taken: string;
  err_already_member: string;
  err_last_owner: string;
  err_sole_owner: string;
  err_invalid_slug: string;
  err_invalid_role: string;
  err_reserved_email: string;
  err_unauthenticated: string;
  err_permission_denied: string;
  err_invalid_credentials: string;
  err_invalid_csrf: string;
  err_rate_limited: string;
  err_unavailable: string;
  err_unimplemented: string;
  err_unknown: string;

  err_code_canceled: string;
  err_code_timeout: string;
  err_code_unavailable: string;
  err_code_unknown: string;
}
