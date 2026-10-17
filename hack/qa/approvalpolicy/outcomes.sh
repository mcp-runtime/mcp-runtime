#!/usr/bin/env bash
# Complete result classifications, not substrings: NotReady must never pass Ready.
approval_outcome_matches() {
  case "$1" in
    Ready) [[ "$2" == Ready || "$2" == Approved,Ready || "$2" == Ready,Approved ]] ;;
    Denied) [[ "$2" == Denied ]] ;;
    Pending) [[ "$2" == 'Pending(never approved, no certificate issued)' ]] ;;
    NotReady) [[ "$2" == 'NotReady(never approved)' ]] ;;
    *) return 1 ;;
  esac
}
