//go:build windows

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package protectedfile

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
func privateDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
}
func trustedPrincipal(value, current string) bool {
	return value == current || value == "S-1-5-18" || value == "S-1-5-32-544"
}
func checkOwner(sd *windows.SECURITY_DESCRIPTOR) error {
	current, err := currentSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !trustedPrincipal(owner.String(), current) {
		return errors.New("protected path has an untrusted owner")
	}
	return nil
}
func checkSecurity(sd *windows.SECURITY_DESCRIPTOR) error {
	if err := checkOwner(sd); err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("protected file DACL must disable inheritance")
	}
	current, err := currentSID()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return errors.New("protected path has no access restrictions")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("protected path has an unsupported access grant")
		}
		principal := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Mask != 0 && !trustedPrincipal(principal.String(), current) {
			return errors.New("protected path grants access to another identity")
		}
	}
	return nil
}
func securityForHandle(handle windows.Handle) (*windows.SECURITY_DESCRIPTOR, error) {
	return windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
}
func checkHandle(handle windows.Handle) error {
	sd, err := securityForHandle(handle)
	if err != nil {
		return err
	}
	return checkSecurity(sd)
}
func protectHandle(handle windows.Handle) error {
	existing, err := securityForHandle(handle)
	if err != nil {
		return err
	}
	// Changing the DACL cannot remove the original owner's power to restore it.
	sd, err := protectionDescriptor(existing)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	return checkHandle(handle)
}

// Protect validates ownership and changes the ACL of the inspected object itself.
func Protect(path string) error {
	file, err := openCheckedPath(path, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN, 0, nil)
	if err != nil {
		return err
	}
	defer file.Close()
	return protectHandle(windows.Handle(file.Fd()))
}

// Check validates the object reached by pinned, non-reparse directory handles.
func Check(path string) error {
	file, err := openCheckedPath(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN, 0, nil)
	if err != nil {
		return err
	}
	defer file.Close()
	return checkHandle(windows.Handle(file.Fd()))
}

func protectionDescriptor(existing *windows.SECURITY_DESCRIPTOR) (*windows.SECURITY_DESCRIPTOR, error) {
	if err := checkOwner(existing); err != nil {
		return nil, err
	}
	return privateDescriptor()
}
