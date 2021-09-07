package rpc

/*
	Sliver Implant Framework
	Copyright (C) 2021  Bishop Fox

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU General Public License as published by
	the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU General Public License for more details.

	You should have received a copy of the GNU General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

import (
	"context"

	"github.com/bishopfox/sliver/protobuf/clientpb"
	"github.com/bishopfox/sliver/protobuf/commonpb"
	"github.com/bishopfox/sliver/server/db"
	"github.com/bishopfox/sliver/server/log"
)

var (
	beaconRpcLog = log.NamedLogger("rpc", "beacons")
)

// GetBeacons - Get a list of beacons from the database
func (rpc *Server) GetBeacons(ctx context.Context, req *commonpb.Empty) (*clientpb.Beacons, error) {
	dbBeacons, err := db.ListBeacons()
	if err != nil {
		beaconRpcLog.Errorf("Failed to find db beacons: %s", err)
		return nil, err
	}
	beacons := []*clientpb.Beacon{}
	for _, beacon := range dbBeacons {
		beacons = append(beacons, beacon.ToProtobuf())
	}
	return &clientpb.Beacons{Beacons: beacons}, nil
}

// GetBeaconTasks - Get a list of tasks for a specific beacon
func (rpc *Server) GetBeaconTasks(ctx context.Context, req *clientpb.Beacon) (*clientpb.BeaconTasks, error) {

	return nil, nil
}

// GetBeaconTaskContent - Get the content of a specific task
func (rpc *Server) GetBeaconTaskContent(ctx context.Context, req *clientpb.BeaconTask) (*clientpb.BeaconTask, error) {

	return nil, nil
}
